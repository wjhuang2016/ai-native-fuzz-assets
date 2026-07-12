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
	"regexp"
	"sort"
	"strings"
)

var ownerName = regexp.MustCompile(`(?i)(buffer|batch|pending|queue|record|event|message|item|file|task|checkpoint|cursor|offset|watermark|last|next)`)

type candidate struct {
	Path       string `json:"path"`
	Function   string `json:"function"`
	ErrorLine  int    `json:"error_line"`
	ResetLine  int    `json:"reset_line"`
	Owner      string `json:"owner"`
	ResetShape string `json:"reset_shape"`
}

func exprName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		prefix := exprName(x.X)
		if prefix == "" {
			return x.Sel.Name
		}
		return prefix + "." + x.Sel.Name
	case *ast.IndexExpr:
		return exprName(x.X)
	case *ast.IndexListExpr:
		return exprName(x.X)
	}
	return ""
}

func isErrName(name string) bool {
	name = strings.ToLower(name)
	return name == "err" || strings.HasSuffix(name, "err") || strings.HasSuffix(name, "error")
}

func checkedErr(cond ast.Expr) string {
	var found string
	ast.Inspect(cond, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if ok && isErrName(id.Name) {
			found = id.Name
			return false
		}
		return found == ""
	})
	return found
}

func blockTerminates(block *ast.BlockStmt) bool {
	if block == nil || len(block.List) == 0 {
		return false
	}
	switch block.List[len(block.List)-1].(type) {
	case *ast.ReturnStmt, *ast.BranchStmt:
		return true
	}
	return false
}

func resetShape(rhs ast.Expr) string {
	switch x := rhs.(type) {
	case *ast.Ident:
		if x.Name == "nil" || x.Name == "false" {
			return x.Name
		}
	case *ast.BasicLit:
		if x.Value == "0" || x.Value == `""` {
			return x.Value
		}
	case *ast.CallExpr:
		name := exprName(x.Fun)
		if name == "make" || name == "new" || strings.Contains(strings.ToLower(name), "reset") {
			return name
		}
	case *ast.CompositeLit:
		if len(x.Elts) == 0 {
			return "empty-composite"
		}
	case *ast.SliceExpr:
		if x.Low == nil && x.High != nil {
			if lit, ok := x.High.(*ast.BasicLit); ok && lit.Value == "0" {
				return "slice-to-zero"
			}
		}
	}
	return ""
}

func scanFunction(fset *token.FileSet, rel string, fnName string, body *ast.BlockStmt, out *[]candidate) {
	if body == nil {
		return
	}
	var checks []*ast.IfStmt
	ast.Inspect(body, func(node ast.Node) bool {
		ifStmt, ok := node.(*ast.IfStmt)
		if ok && checkedErr(ifStmt.Cond) != "" && !blockTerminates(ifStmt.Body) {
			checks = append(checks, ifStmt)
		}
		return true
	})
	for _, check := range checks {
		errName := checkedErr(check.Cond)
		ast.Inspect(body, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok || assign.Pos() <= check.End() || len(assign.Lhs) != len(assign.Rhs) {
				return true
			}
			for i := range assign.Lhs {
				owner := exprName(assign.Lhs[i])
				if !ownerName.MatchString(owner) {
					continue
				}
				shape := resetShape(assign.Rhs[i])
				if shape == "" {
					continue
				}
				*out = append(*out, candidate{
					Path: rel, Function: fnName,
					ErrorLine: fset.Position(check.Pos()).Line,
					ResetLine: fset.Position(assign.Pos()).Line,
					Owner:     owner, ResetShape: shape + " after " + errName,
				})
			}
			return true
		})
	}
}

func main() {
	root := flag.String("root", ".", "Go source root")
	flag.Parse()
	fset := token.NewFileSet()
	var found []candidate
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
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(*root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok {
				scanFunction(fset, rel, fn.Name.Name, fn.Body, &found)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Path != found[j].Path {
			return found[i].Path < found[j].Path
		}
		if found[i].Function != found[j].Function {
			return found[i].Function < found[j].Function
		}
		return found[i].ResetLine < found[j].ResetLine
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(found); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
