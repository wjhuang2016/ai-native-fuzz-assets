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
	Path        string   `json:"path"`
	Function    string   `json:"function"`
	IfLine      int      `json:"if_line"`
	ReturnLine  int      `json:"return_line"`
	Checked     []string `json:"checked"`
	ReturnedIDs []string `json:"returned_ids"`
	ReturnExpr  string   `json:"return_expr"`
	NextCalls   []string `json:"next_calls"`
}

func comparedToNil(expr ast.Expr) []string {
	b, ok := expr.(*ast.BinaryExpr)
	if !ok || b.Op != token.NEQ {
		return nil
	}
	var id *ast.Ident
	if x, ok := b.X.(*ast.Ident); ok {
		if y, nilOK := b.Y.(*ast.Ident); nilOK && y.Name == "nil" {
			id = x
		}
	}
	if y, ok := b.Y.(*ast.Ident); ok {
		if x, nilOK := b.X.(*ast.Ident); nilOK && x.Name == "nil" {
			id = y
		}
	}
	if id == nil {
		return nil
	}
	lower := strings.ToLower(id.Name)
	if lower != "e" && lower != "err" && !strings.HasPrefix(lower, "err") && !strings.HasSuffix(lower, "err") && !strings.HasSuffix(lower, "error") {
		return nil
	}
	return []string{id.Name}
}

func idsIn(node ast.Node) []string {
	set := map[string]struct{}{}
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			set[id.Name] = struct{}{}
		}
		return true
	})
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

func exprText(fset *token.FileSet, src []byte, node ast.Node) string {
	start := fset.Position(node.Pos()).Offset
	end := fset.Position(node.End()).Offset
	if start < 0 || end < start || end > len(src) {
		return ""
	}
	return strings.TrimSpace(string(src[start:end]))
}

func calledNames(node ast.Node) []string {
	set := map[string]struct{}{}
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		if name != "" {
			set[name] = struct{}{}
		}
		return true
	})
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func errorResultPositions(fn *ast.FuncDecl) (int, []int) {
	if fn.Type.Results == nil {
		return 0, nil
	}
	count := 0
	var positions []int
	for _, field := range fn.Type.Results.List {
		width := len(field.Names)
		if width == 0 {
			width = 1
		}
		id, isIdent := field.Type.(*ast.Ident)
		for range width {
			if isIdent && id.Name == "error" {
				positions = append(positions, count)
			}
			count++
		}
	}
	return count, positions
}

func scanBody(path, fnName string, fset *token.FileSet, src []byte, body *ast.BlockStmt, resultCount int, errorPositions []int, out *[]hit) {
	if len(errorPositions) == 0 {
		return
	}
	for i, stmt := range body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if !ok {
			continue
		}
		checked := comparedToNil(ifStmt.Cond)
		if len(checked) == 0 {
			continue
		}
		var returns []*ast.ReturnStmt
		for _, bodyStmt := range ifStmt.Body.List {
			if r, ok := bodyStmt.(*ast.ReturnStmt); ok {
				returns = append(returns, r)
			}
		}
		for _, ret := range returns {
			if len(ret.Results) != resultCount {
				continue
			}
			var errorExprs []ast.Expr
			for _, pos := range errorPositions {
				if pos < len(ret.Results) {
					errorExprs = append(errorExprs, ret.Results[pos])
				}
			}
			returnedIDs := []string{}
			for _, expr := range errorExprs {
				returnedIDs = append(returnedIDs, idsIn(expr)...)
			}
			sort.Strings(returnedIDs)
			usesChecked := false
			for _, name := range checked {
				usesChecked = usesChecked || contains(returnedIDs, name)
			}
			if usesChecked {
				continue
			}
			interestingReturn := false
			for _, name := range returnedIDs {
				lower := strings.ToLower(name)
				if lower == "err" || lower == "e" || strings.HasSuffix(lower, "err") || strings.HasSuffix(lower, "error") || lower == "nil" {
					interestingReturn = true
				}
			}
			if !interestingReturn {
				continue
			}
			nextCalls := []string{}
			for j := i + 1; j < len(body.List) && j <= i+8; j++ {
				nextCalls = append(nextCalls, calledNames(body.List[j])...)
			}
			nextSet := map[string]struct{}{}
			for _, name := range nextCalls {
				nextSet[name] = struct{}{}
			}
			nextCalls = nextCalls[:0]
			for name := range nextSet {
				nextCalls = append(nextCalls, name)
			}
			sort.Strings(nextCalls)
			*out = append(*out, hit{
				Path:        path,
				Function:    fnName,
				IfLine:      fset.Position(ifStmt.If).Line,
				ReturnLine:  fset.Position(ret.Return).Line,
				Checked:     checked,
				ReturnedIDs: returnedIDs,
				ReturnExpr:  exprText(fset, src, ret),
				NextCalls:   nextCalls,
			})
		}
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
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "third_party" || name == "bazel-out" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(*root, path)
		if err != nil {
			rel = path
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			resultCount, errorPositions := errorResultPositions(fn)
			scanBody(rel, fn.Name.Name, fset, src, fn.Body, resultCount, errorPositions, &hits)
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
		return hits[i].IfLine < hits[j].IfLine
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(hits); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
