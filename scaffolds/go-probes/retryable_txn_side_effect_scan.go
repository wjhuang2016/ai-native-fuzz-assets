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

type mutation struct {
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

type hit struct {
	Path      string     `json:"path"`
	Function  string     `json:"function"`
	Line      int        `json:"line"`
	API       string     `json:"retry_api"`
	Mutations []mutation `json:"captured_mutations,omitempty"`
	Calls     []callSite `json:"calls"`
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
		return exprName(x.X) + "[]"
	case *ast.IndexListExpr:
		return exprName(x.X) + "[]"
	case *ast.StarExpr:
		return "*" + exprName(x.X)
	case *ast.ParenExpr:
		return exprName(x.X)
	}
	return ""
}

func baseIdent(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return baseIdent(x.X)
	case *ast.IndexExpr:
		return baseIdent(x.X)
	case *ast.IndexListExpr:
		return baseIdent(x.X)
	case *ast.StarExpr:
		return baseIdent(x.X)
	case *ast.ParenExpr:
		return baseIdent(x.X)
	case *ast.UnaryExpr:
		return baseIdent(x.X)
	}
	return ""
}

func addFieldNames(dst map[string]bool, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			dst[name.Name] = true
		}
	}
}

func closureLocals(fn *ast.FuncLit) map[string]bool {
	locals := map[string]bool{"_": true}
	addFieldNames(locals, fn.Type.Params)
	addFieldNames(locals, fn.Type.Results)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.FuncLit:
			if x != fn {
				return false
			}
		case *ast.ValueSpec:
			for _, name := range x.Names {
				locals[name.Name] = true
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if name, ok := lhs.(*ast.Ident); ok {
						locals[name.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				if name, ok := x.Key.(*ast.Ident); ok {
					locals[name.Name] = true
				}
				if name, ok := x.Value.(*ast.Ident); ok {
					locals[name.Name] = true
				}
			}
		}
		return true
	})
	return locals
}

func isCaptured(expr ast.Expr, locals, imports map[string]bool) bool {
	base := baseIdent(expr)
	return base != "" && !locals[base] && !imports[base]
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

func retryClosure(call *ast.CallExpr) (string, *ast.FuncLit, bool) {
	name := callName(call.Fun)
	switch {
	case strings.HasSuffix(name, "RunInNewTxn") && len(call.Args) >= 4:
		retry, ok := call.Args[2].(*ast.Ident)
		if !ok || retry.Name != "true" {
			return "", nil, false
		}
		fn, ok := call.Args[3].(*ast.FuncLit)
		return name, fn, ok
	case strings.HasSuffix(name, "RunWithRetry") && len(call.Args) > 0:
		fn, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
		return name, fn, ok
	default:
		return "", nil, false
	}
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
		imports := make(map[string]bool)
		for _, spec := range file.Imports {
			if spec.Name != nil {
				imports[spec.Name.Name] = true
				continue
			}
			pathValue := strings.Trim(spec.Path.Value, "\"")
			imports[filepath.Base(pathValue)] = true
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			api, fn, ok := retryClosure(call)
			if !ok {
				return true
			}
			locals := closureLocals(fn)
			var calls []callSite
			var mutations []mutation
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				if nested, ok := inner.(*ast.FuncLit); ok && nested != fn {
					return false
				}
				switch x := inner.(type) {
				case *ast.AssignStmt:
					if x.Tok != token.DEFINE {
						for _, lhs := range x.Lhs {
							if isCaptured(lhs, locals, imports) {
								mutations = append(mutations, mutation{
									Line: fset.Position(lhs.Pos()).Line, Kind: "assign", Target: exprName(lhs),
								})
							}
						}
					}
				case *ast.IncDecStmt:
					if isCaptured(x.X, locals, imports) {
						mutations = append(mutations, mutation{
							Line: fset.Position(x.Pos()).Line, Kind: "incdec", Target: exprName(x.X),
						})
					}
				}
				innerCall, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := callName(innerCall.Fun)
				if name != "" {
					calls = append(calls, callSite{Line: fset.Position(innerCall.Pos()).Line, Name: name})
				}
				if selector, ok := innerCall.Fun.(*ast.SelectorExpr); ok && isCaptured(selector.X, locals, imports) {
					mutations = append(mutations, mutation{
						Line: fset.Position(innerCall.Pos()).Line,
						Kind: "receiver_call", Target: exprName(selector.X) + "." + selector.Sel.Name,
					})
				}
				if ident, ok := innerCall.Fun.(*ast.Ident); ok && (ident.Name == "delete" || ident.Name == "clear" || ident.Name == "copy") && len(innerCall.Args) > 0 && isCaptured(innerCall.Args[0], locals, imports) {
					mutations = append(mutations, mutation{
						Line: fset.Position(innerCall.Pos()).Line,
						Kind: "builtin_" + ident.Name, Target: exprName(innerCall.Args[0]),
					})
				}
				for _, arg := range innerCall.Args {
					unary, ok := arg.(*ast.UnaryExpr)
					if ok && unary.Op == token.AND && isCaptured(unary.X, locals, imports) {
						mutations = append(mutations, mutation{
							Line: fset.Position(arg.Pos()).Line,
							Kind: "address_argument", Target: "&" + exprName(unary.X),
						})
					}
				}
				return true
			})
			sort.Slice(calls, func(i, j int) bool {
				if calls[i].Line != calls[j].Line {
					return calls[i].Line < calls[j].Line
				}
				return calls[i].Name < calls[j].Name
			})
			sort.Slice(mutations, func(i, j int) bool {
				if mutations[i].Line != mutations[j].Line {
					return mutations[i].Line < mutations[j].Line
				}
				if mutations[i].Kind != mutations[j].Kind {
					return mutations[i].Kind < mutations[j].Kind
				}
				return mutations[i].Target < mutations[j].Target
			})
			hits = append(hits, hit{
				Path: rel, Function: enclosingFunction(file, call.Pos()),
				Line: fset.Position(call.Pos()).Line, API: api, Mutations: mutations, Calls: calls,
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
