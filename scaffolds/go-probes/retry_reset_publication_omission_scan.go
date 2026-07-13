package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type fieldInfo struct {
	Name    string `json:"name"`
	Line    int    `json:"line"`
	Comment string `json:"comment,omitempty"`
}

type mutationSite struct {
	Path     string `json:"path"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`
	Target   string `json:"target"`
}

type resetInfo struct {
	Path         string
	Function     string
	ReceiverType string
	Line         int
	ResetFields  map[string]bool
	Calls        map[string]bool
}

type candidate struct {
	Path              string         `json:"path"`
	Function          string         `json:"function"`
	Line              int            `json:"line"`
	ReceiverType      string         `json:"receiver_type"`
	ValueField        fieldInfo      `json:"value_field"`
	FlagField         fieldInfo      `json:"flag_field"`
	ResetValue        bool           `json:"reset_value"`
	ResetFlag         bool           `json:"reset_flag"`
	ValueMutations    []mutationSite `json:"value_mutations"`
	FlagMutations     []mutationSite `json:"flag_mutations"`
	SameFunctionWrite bool           `json:"same_function_write"`
	Score             int            `json:"score"`
}

func typeName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return typeName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return typeName(x.X)
	case *ast.IndexListExpr:
		return typeName(x.X)
	case *ast.ParenExpr:
		return typeName(x.X)
	}
	return ""
}

func receiver(fn *ast.FuncDecl) (string, string) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 {
		return "", ""
	}
	return fn.Recv.List[0].Names[0].Name, typeName(fn.Recv.List[0].Type)
}

func selectorParts(expr ast.Expr) []string {
	switch x := expr.(type) {
	case *ast.Ident:
		return []string{x.Name}
	case *ast.SelectorExpr:
		return append(selectorParts(x.X), x.Sel.Name)
	case *ast.IndexExpr:
		return selectorParts(x.X)
	case *ast.IndexListExpr:
		return selectorParts(x.X)
	case *ast.StarExpr:
		return selectorParts(x.X)
	case *ast.ParenExpr:
		return selectorParts(x.X)
	}
	return nil
}

func targetName(expr ast.Expr) string {
	parts := selectorParts(expr)
	return strings.Join(parts, ".")
}

func finalField(expr ast.Expr) string {
	parts := selectorParts(expr)
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

func directReceiverField(expr ast.Expr, recv string) string {
	parts := selectorParts(expr)
	if len(parts) < 2 || parts[0] != recv {
		return ""
	}
	return parts[1]
}

func commentText(field *ast.Field) string {
	var parts []string
	for _, group := range []*ast.CommentGroup{field.Doc, field.Comment} {
		if group == nil {
			continue
		}
		for _, comment := range group.List {
			parts = append(parts, strings.TrimSpace(strings.TrimPrefix(comment.Text, "//")))
		}
	}
	return strings.Join(parts, " ")
}

func enclosingFunction(file *ast.File, pos token.Pos) string {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Pos() <= pos && pos <= fn.End() {
			return fn.Name.Name
		}
	}
	return ""
}

func isRetryReset(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "reset") && strings.Contains(lower, "retry")
}

func isMutatingMethod(name string) bool {
	for _, prefix := range []string{
		"Add", "Append", "Clear", "Delete", "Inc", "Put", "Remove", "Reset", "Set", "Store", "Truncate", "Update", "Write",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func trimSites(sites []mutationSite, limit int) []mutationSite {
	if len(sites) <= limit {
		return sites
	}
	return sites[:limit]
}

func main() {
	root := flag.String("root", ".", "Go source root")
	flag.Parse()

	fset := token.NewFileSet()
	structs := make(map[string]map[string]fieldInfo)
	mutations := make(map[string][]mutationSite)
	var resets []resetInfo

	err := filepath.WalkDir(*root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "third_party", "bazel-out":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(*root, path)
		if relErr != nil {
			rel = path
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				fields := make(map[string]fieldInfo)
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						fields[name.Name] = fieldInfo{Name: name.Name, Line: fset.Position(name.Pos()).Line, Comment: commentText(field)}
					}
				}
				structs[typeSpec.Name.Name] = fields
			}
		}

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recv, recvType := receiver(fn)
			var reset *resetInfo
			if recv != "" && recvType != "" && isRetryReset(fn.Name.Name) {
				reset = &resetInfo{
					Path: rel, Function: fn.Name.Name, ReceiverType: recvType,
					Line: fset.Position(fn.Pos()).Line, ResetFields: make(map[string]bool), Calls: make(map[string]bool),
				}
			}

			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch x := node.(type) {
				case *ast.AssignStmt:
					if x.Tok == token.DEFINE {
						return true
					}
					for _, lhs := range x.Lhs {
						field := finalField(lhs)
						if field != "" {
							mutations[field] = append(mutations[field], mutationSite{
								Path: rel, Function: fn.Name.Name, Line: fset.Position(lhs.Pos()).Line,
								Kind: "assign", Target: targetName(lhs),
							})
						}
						if reset != nil {
							if direct := directReceiverField(lhs, recv); direct != "" {
								reset.ResetFields[direct] = true
							}
						}
					}
				case *ast.IncDecStmt:
					field := finalField(x.X)
					if field != "" {
						mutations[field] = append(mutations[field], mutationSite{
							Path: rel, Function: fn.Name.Name, Line: fset.Position(x.Pos()).Line,
							Kind: "incdec", Target: targetName(x.X),
						})
					}
					if reset != nil {
						if direct := directReceiverField(x.X, recv); direct != "" {
							reset.ResetFields[direct] = true
						}
					}
				case *ast.CallExpr:
					sel, ok := x.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if reset != nil && targetName(sel.X) == recv && isRetryReset(sel.Sel.Name) {
						reset.Calls[sel.Sel.Name] = true
					}
					if !isMutatingMethod(sel.Sel.Name) {
						return true
					}
					field := finalField(sel.X)
					if field != "" {
						mutations[field] = append(mutations[field], mutationSite{
							Path: rel, Function: enclosingFunction(file, x.Pos()), Line: fset.Position(x.Pos()).Line,
							Kind: "receiver_call", Target: targetName(sel.X) + "." + sel.Sel.Name,
						})
					}
					if reset != nil {
						if direct := directReceiverField(sel.X, recv); direct != "" {
							reset.ResetFields[direct] = true
						}
					}
				}
				return true
			})
			if reset != nil {
				resets = append(resets, *reset)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	flagSuffixes := []string{"Changed", "Present", "Dirty", "Valid", "Set"}
	nestedReset := make(map[string]map[string]bool)
	for _, reset := range resets {
		if nestedReset[reset.ReceiverType] == nil {
			nestedReset[reset.ReceiverType] = make(map[string]bool)
		}
		for called := range reset.Calls {
			nestedReset[reset.ReceiverType][called] = true
		}
	}
	var candidates []candidate
	for _, reset := range resets {
		if nestedReset[reset.ReceiverType][reset.Function] {
			continue
		}
		fields := structs[reset.ReceiverType]
		for flagName, flagField := range fields {
			var valueName string
			for _, suffix := range flagSuffixes {
				if strings.HasSuffix(flagName, suffix) && len(flagName) > len(suffix) {
					valueName = strings.TrimSuffix(flagName, suffix)
					break
				}
			}
			if valueName == "" {
				continue
			}
			valueField, ok := fields[valueName]
			if !ok {
				continue
			}
			valueSites := mutations[valueName]
			flagSites := mutations[flagName]
			if len(valueSites) == 0 || len(flagSites) == 0 {
				continue
			}
			resetValue := reset.ResetFields[valueName]
			resetFlag := reset.ResetFields[flagName]
			if resetValue && resetFlag {
				continue
			}
			sameFunction := false
			for _, valueSite := range valueSites {
				for _, flagSite := range flagSites {
					if valueSite.Path == flagSite.Path && valueSite.Function == flagSite.Function {
						sameFunction = true
					}
				}
			}
			score := 60
			if !resetValue && !resetFlag {
				score += 15
			} else {
				score += 25
			}
			if sameFunction {
				score += 15
			}
			comments := strings.ToLower(valueField.Comment + " " + flagField.Comment)
			if strings.Contains(comments, "statement") || strings.Contains(comments, "publish") || strings.Contains(comments, "current") {
				score += 10
			}
			candidates = append(candidates, candidate{
				Path: reset.Path, Function: reset.Function, Line: reset.Line, ReceiverType: reset.ReceiverType,
				ValueField: valueField, FlagField: flagField, ResetValue: resetValue, ResetFlag: resetFlag,
				ValueMutations: trimSites(valueSites, 12), FlagMutations: trimSites(flagSites, 12),
				SameFunctionWrite: sameFunction, Score: score,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Path != candidates[j].Path {
			return candidates[i].Path < candidates[j].Path
		}
		return candidates[i].ValueField.Name < candidates[j].ValueField.Name
	})

	output := map[string]any{
		"scanner_schema": "retry-reset-publication-omission-v1",
		"source_root":    *root,
		"count":          len(candidates),
		"candidates":     candidates,
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
