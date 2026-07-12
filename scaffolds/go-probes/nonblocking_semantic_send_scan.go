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

type hit struct {
	Path     string `json:"path"`
	Function string `json:"function"`
	Line     int    `json:"line"`
	Channel  string `json:"channel"`
	Payload  string `json:"payload"`
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
	case *ast.CallExpr:
		return exprName(x.Fun) + "()"
	case *ast.IndexExpr:
		return exprName(x.X)
	case *ast.UnaryExpr:
		return exprName(x.X)
	}
	return fmt.Sprintf("%T", expr)
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
		rel, _ := filepath.Rel(*root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectStmt)
				if !ok {
					return true
				}
				hasDefault := false
				var sends []*ast.SendStmt
				for _, stmt := range sel.Body.List {
					clause := stmt.(*ast.CommClause)
					if clause.Comm == nil {
						hasDefault = true
						continue
					}
					if send, ok := clause.Comm.(*ast.SendStmt); ok {
						sends = append(sends, send)
					}
				}
				if !hasDefault {
					return true
				}
				for _, send := range sends {
					hits = append(hits, hit{
						Path: rel, Function: fn.Name.Name,
						Line:    fset.Position(send.Pos()).Line,
						Channel: exprName(send.Chan), Payload: exprName(send.Value),
					})
				}
				return true
			})
		}
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
