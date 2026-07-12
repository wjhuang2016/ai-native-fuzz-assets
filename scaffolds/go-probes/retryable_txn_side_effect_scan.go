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

type callSite struct {
	Line int    `json:"line"`
	Name string `json:"name"`
}

type hit struct {
	Path     string     `json:"path"`
	Function string     `json:"function"`
	Line     int        `json:"line"`
	Calls    []callSite `json:"calls"`
}

func callName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		prefix := callName(x.X)
		if prefix == "" {
			return x.Sel.Name
		}
		return prefix + "." + x.Sel.Name
	case *ast.CallExpr:
		return callName(x.Fun)
	case *ast.IndexExpr:
		return callName(x.X)
	case *ast.IndexListExpr:
		return callName(x.X)
	}
	return ""
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

func main() {
	root := flag.String("root", ".", "Go source root")
	flag.Parse()
	fset := token.NewFileSet()
	var hits []hit
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
		rel, relErr := filepath.Rel(*root, path)
		if relErr != nil {
			rel = path
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !strings.HasSuffix(callName(call.Fun), "RunInNewTxn") || len(call.Args) < 4 {
				return true
			}
			retry, ok := call.Args[2].(*ast.Ident)
			if !ok || retry.Name != "true" {
				return true
			}
			fn, ok := call.Args[3].(*ast.FuncLit)
			if !ok {
				return true
			}
			var calls []callSite
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				innerCall, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(innerCall.Fun)
				if name != "" {
					calls = append(calls, callSite{Line: fset.Position(innerCall.Pos()).Line, Name: name})
				}
				return true
			})
			sort.Slice(calls, func(i, j int) bool {
				if calls[i].Line != calls[j].Line {
					return calls[i].Line < calls[j].Line
				}
				return calls[i].Name < calls[j].Name
			})
			hits = append(hits, hit{
				Path: rel, Function: enclosingFunction(file, call.Pos()),
				Line: fset.Position(call.Pos()).Line, Calls: calls,
			})
			return false
		})
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Line < hits[j].Line
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hits); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
