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

var terminalActions = map[string]struct{}{
	"Close": {}, "Commit": {}, "Finalize": {}, "Flush": {}, "Sync": {}, "Wait": {},
}

type deferredTerminalCandidate struct {
	Path             string   `json:"path"`
	Function         string   `json:"function"`
	FunctionLine     int      `json:"function_line"`
	DeferLine        int      `json:"defer_line"`
	ActionLine       int      `json:"action_line"`
	Action           string   `json:"action"`
	ErrorResultNamed bool     `json:"error_result_named"`
	MutatesResult    bool     `json:"mutates_result"`
	HandlingCalls    []string `json:"handling_calls,omitempty"`
}

func expressionName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		prefix := expressionName(x.X)
		if prefix == "" {
			return x.Sel.Name
		}
		return prefix + "." + x.Sel.Name
	case *ast.CallExpr:
		return expressionName(x.Fun) + "()"
	case *ast.IndexExpr:
		return expressionName(x.X)
	case *ast.IndexListExpr:
		return expressionName(x.X)
	case *ast.ParenExpr:
		return expressionName(x.X)
	}
	return ""
}

func terminalCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if _, ok := terminalActions[sel.Sel.Name]; !ok {
		return "", false
	}
	return expressionName(call.Fun), true
}

func errorResultNames(fn *ast.FuncDecl) map[string]struct{} {
	names := make(map[string]struct{})
	if fn.Type.Results == nil {
		return names
	}
	for _, field := range fn.Type.Results.List {
		id, ok := field.Type.(*ast.Ident)
		if !ok || id.Name != "error" {
			continue
		}
		for _, name := range field.Names {
			names[name.Name] = struct{}{}
		}
	}
	return names
}

func assignedNames(node ast.Node) map[string]struct{} {
	names := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok {
				names[id.Name] = struct{}{}
			}
		}
		return true
	})
	return names
}

func callsIn(node ast.Node) []string {
	set := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := expressionName(call.Fun)
		if name != "" {
			set[name] = struct{}{}
		}
		return true
	})
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func scanFunction(fset *token.FileSet, rel string, fn *ast.FuncDecl, out *[]deferredTerminalCandidate) {
	if fn.Body == nil {
		return
	}
	resultNames := errorResultNames(fn)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		deferStmt, ok := node.(*ast.DeferStmt)
		if !ok {
			return true
		}
		lit, ok := deferStmt.Call.Fun.(*ast.FuncLit)
		if !ok || lit.Body == nil {
			return true
		}
		assigned := assignedNames(lit.Body)
		mutatesResult := false
		for name := range resultNames {
			if _, ok := assigned[name]; ok {
				mutatesResult = true
				break
			}
		}
		ast.Inspect(lit.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			action, ok := terminalCall(call)
			if !ok {
				return true
			}
			*out = append(*out, deferredTerminalCandidate{
				Path: rel, Function: fn.Name.Name,
				FunctionLine: fset.Position(fn.Pos()).Line,
				DeferLine:    fset.Position(deferStmt.Pos()).Line,
				ActionLine:   fset.Position(call.Pos()).Line,
				Action:       action, ErrorResultNamed: len(resultNames) > 0,
				MutatesResult: mutatesResult, HandlingCalls: callsIn(lit.Body),
			})
			return true
		})
		return false
	})
}

func main() {
	root := flag.String("root", ".", "Go source root")
	flag.Parse()
	fset := token.NewFileSet()
	var found []deferredTerminalCandidate
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
			if fn, ok := decl.(*ast.FuncDecl); ok {
				scanFunction(fset, rel, fn, &found)
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
		if found[i].FunctionLine != found[j].FunctionLine {
			return found[i].FunctionLine < found[j].FunctionLine
		}
		return found[i].ActionLine < found[j].ActionLine
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(found); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
