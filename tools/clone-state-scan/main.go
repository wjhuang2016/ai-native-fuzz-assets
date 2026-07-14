// clone-state-scan finds mutable-looking Go struct fields that are copied by Clone
// and then used by evaluation methods. It generates candidates, not bug verdicts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type fieldInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Line int    `json:"line"`
}

type useInfo struct {
	Method string `json:"method"`
	Field  string `json:"field"`
	Use    string `json:"use"`
	Line   int    `json:"line"`
}

type candidate struct {
	Type      string      `json:"type"`
	File      string      `json:"file"`
	Line      int         `json:"line"`
	Fields    []fieldInfo `json:"fields"`
	CloneLine int         `json:"clone_line"`
	CloneMode string      `json:"clone_mode"`
	EvalUses  []useInfo   `json:"eval_uses"`
}

type structInfo struct {
	name   string
	file   string
	line   int
	fields map[string]fieldInfo
}

type methodInfo struct {
	receiver string
	recvName string
	name     string
	file     string
	line     int
	decl     *ast.FuncDecl
}

func main() {
	root := flag.String("root", ".", "source root")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{"."}
	}

	fset := token.NewFileSet()
	structs := make(map[string]*structInfo)
	var methods []methodInfo
	for _, dir := range dirs {
		base := filepath.Join(*root, dir)
		err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			rel, relErr := filepath.Rel(*root, path)
			if relErr != nil {
				return relErr
			}
			collectFile(fset, filepath.ToSlash(rel), file, structs, &methods)
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	results := analyze(fset, structs, methods)
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(results); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func collectFile(
	fset *token.FileSet,
	path string,
	file *ast.File,
	structs map[string]*structInfo,
	methods *[]methodInfo,
) {
	for _, decl := range file.Decls {
		switch node := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				info := &structInfo{
					name:   typeSpec.Name.Name,
					file:   path,
					line:   fset.Position(typeSpec.Pos()).Line,
					fields: make(map[string]fieldInfo),
				}
				for _, field := range structType.Fields.List {
					if len(field.Names) == 0 || !mutableLooking(field.Type, field.Names) {
						continue
					}
					for _, name := range field.Names {
						info.fields[name.Name] = fieldInfo{
							Name: name.Name,
							Type: renderNode(fset, field.Type),
							Line: fset.Position(field.Pos()).Line,
						}
					}
				}
				if len(info.fields) > 0 {
					structs[info.name] = info
				}
			}
		case *ast.FuncDecl:
			if node.Recv == nil || len(node.Recv.List) != 1 || len(node.Recv.List[0].Names) != 1 {
				continue
			}
			receiver := receiverType(node.Recv.List[0].Type)
			if receiver == "" {
				continue
			}
			*methods = append(*methods, methodInfo{
				receiver: receiver,
				recvName: node.Recv.List[0].Names[0].Name,
				name:     node.Name.Name,
				file:     path,
				line:     fset.Position(node.Pos()).Line,
				decl:     node,
			})
		}
	}
}

func analyze(fset *token.FileSet, structs map[string]*structInfo, methods []methodInfo) []candidate {
	byType := make(map[string][]methodInfo)
	for _, method := range methods {
		byType[method.receiver] = append(byType[method.receiver], method)
	}

	var results []candidate
	for typeName, info := range structs {
		var clone *methodInfo
		for index := range byType[typeName] {
			method := &byType[typeName][index]
			if method.name == "Clone" {
				clone = method
				break
			}
		}
		if clone == nil {
			continue
		}

		cloneFields, wholeCopy := cloneReferences(clone.decl.Body, clone.recvName, info.fields)
		if wholeCopy {
			for field := range info.fields {
				cloneFields[field] = true
			}
		}
		if len(cloneFields) == 0 {
			continue
		}

		var uses []useInfo
		for _, method := range byType[typeName] {
			if !strings.HasPrefix(method.name, "eval") && !strings.HasPrefix(method.name, "vecEval") {
				continue
			}
			uses = append(uses, evalUses(fset, method, cloneFields)...)
		}
		if len(uses) == 0 {
			continue
		}

		fields := make([]fieldInfo, 0, len(cloneFields))
		for field := range cloneFields {
			fields = append(fields, info.fields[field])
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		sort.Slice(uses, func(i, j int) bool {
			if uses[i].Line != uses[j].Line {
				return uses[i].Line < uses[j].Line
			}
			return uses[i].Use < uses[j].Use
		})
		mode := "field-copy"
		if wholeCopy {
			mode = "whole-struct-copy"
		}
		results = append(results, candidate{
			Type:      typeName,
			File:      info.file,
			Line:      info.line,
			Fields:    fields,
			CloneLine: clone.line,
			CloneMode: mode,
			EvalUses:  uses,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		return results[i].Line < results[j].Line
	})
	return results
}

func mutableLooking(expr ast.Expr, names []*ast.Ident) bool {
	switch expr.(type) {
	case *ast.StarExpr, *ast.MapType, *ast.InterfaceType, *ast.ChanType, *ast.FuncType:
		return true
	case *ast.ArrayType:
		return expr.(*ast.ArrayType).Len == nil
	}
	text := strings.ToLower(renderNode(token.NewFileSet(), expr))
	for _, token := range []string{"rng", "cache", "state", "buffer", "buf", "memo", "regexp", "collator", "once", "context", "ctx", "decoder", "encoder", "allocator", "matcher"} {
		if strings.Contains(text, token) {
			return true
		}
	}
	for _, name := range names {
		lower := strings.ToLower(name.Name)
		for _, token := range []string{"rng", "cache", "state", "buffer", "buf", "memo", "regexp", "collator", "once", "ctx", "decoder", "encoder", "allocator", "matcher"} {
			if strings.Contains(lower, token) {
				return true
			}
		}
	}
	return false
}

func cloneReferences(body *ast.BlockStmt, receiver string, fields map[string]fieldInfo) (map[string]bool, bool) {
	refs := make(map[string]bool)
	wholeCopy := false
	ast.Inspect(body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.SelectorExpr:
			ident, ok := value.X.(*ast.Ident)
			if ok && ident.Name == receiver {
				if _, exists := fields[value.Sel.Name]; exists {
					refs[value.Sel.Name] = true
				}
			}
		case *ast.StarExpr:
			ident, ok := value.X.(*ast.Ident)
			if ok && ident.Name == receiver {
				wholeCopy = true
			}
		}
		return true
	})
	return refs, wholeCopy
}

func evalUses(fset *token.FileSet, method methodInfo, fields map[string]bool) []useInfo {
	seen := make(map[string]bool)
	var uses []useInfo
	add := func(field, use string, line int) {
		key := fmt.Sprintf("%s:%s:%d", field, use, line)
		if seen[key] {
			return
		}
		seen[key] = true
		uses = append(uses, useInfo{Method: method.name, Field: field, Use: use, Line: line})
	}
	ast.Inspect(method.decl.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			selector, ok := value.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			field := receiverField(selector.X, method.recvName)
			if fields[field] {
				add(field, "call:"+selector.Sel.Name, fset.Position(value.Pos()).Line)
			}
		case *ast.AssignStmt:
			for _, lhs := range value.Lhs {
				if selector, ok := lhs.(*ast.SelectorExpr); ok {
					field := receiverField(selector, method.recvName)
					if fields[field] {
						add(field, "assign", fset.Position(lhs.Pos()).Line)
					}
				}
			}
		case *ast.IncDecStmt:
			if selector, ok := value.X.(*ast.SelectorExpr); ok {
				field := receiverField(selector, method.recvName)
				if fields[field] {
					add(field, "incdec", fset.Position(value.Pos()).Line)
				}
			}
		}
		return true
	})
	return uses
}

func receiverField(expr ast.Expr, receiver string) string {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		if ident, ok := value.X.(*ast.Ident); ok && ident.Name == receiver {
			return value.Sel.Name
		}
		return receiverField(value.X, receiver)
	case *ast.IndexExpr:
		return receiverField(value.X, receiver)
	}
	return ""
}

func receiverType(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.StarExpr:
		return receiverType(value.X)
	case *ast.Ident:
		return value.Name
	case *ast.IndexExpr:
		return receiverType(value.X)
	case *ast.IndexListExpr:
		return receiverType(value.X)
	}
	return ""
}

func renderNode(fset *token.FileSet, node any) string {
	var builder strings.Builder
	if err := printer.Fprint(&builder, fset, node); err != nil {
		return ""
	}
	return builder.String()
}
