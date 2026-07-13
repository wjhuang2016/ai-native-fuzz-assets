package main

import (
	"bufio"
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
	"strconv"
	"strings"
)

// This scanner finds deferred terminal errors that cannot reach the enclosing
// function's actual error result. It deliberately stops before severity
// classification; callers must trace the retry/task consumer and declare a C3
// durable-state oracle before admitting a target.

var terminalActionScore = map[string]int{
	"Abort": 18, "Close": 12, "Commit": 25, "Finalize": 18,
	"Finish": 16, "Flush": 18, "Release": 12, "Shutdown": 16,
	"Stop": 12, "Sync": 18, "Wait": 10,
}

type errorSlot struct {
	Index int
	Name  string
	Obj   *ast.Object
}

type candidate struct {
	Path                   string   `json:"path"`
	Function               string   `json:"function"`
	FunctionLine           int      `json:"function_line"`
	DeferLine              int      `json:"defer_line"`
	ActionLine             int      `json:"action_line"`
	Action                 string   `json:"action"`
	ActionExpr             string   `json:"action_expr"`
	ErrorResultCount       int      `json:"error_result_count"`
	NamedErrorResults      []string `json:"named_error_results,omitempty"`
	Sink                   string   `json:"sink,omitempty"`
	SinkBinding            string   `json:"sink_binding"`
	PublicErrorPropagation bool     `json:"public_error_propagation"`
	PropagationReason      string   `json:"propagation_reason"`
	NilErrorReturnLines    []int    `json:"nil_error_return_lines,omitempty"`
	DeferredHandlingCalls  []string `json:"deferred_handling_calls,omitempty"`
	ClosedRoot             bool     `json:"closed_root"`
	Score                  int      `json:"score"`
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
	case *ast.IndexListExpr:
		return exprName(x.X)
	case *ast.ParenExpr:
		return exprName(x.X)
	case *ast.StarExpr:
		return "*" + exprName(x.X)
	}
	return ""
}

func sourceText(fset *token.FileSet, src []byte, node ast.Node) string {
	if node == nil {
		return ""
	}
	start := fset.Position(node.Pos()).Offset
	end := fset.Position(node.End()).Offset
	if start < 0 || end < start || end > len(src) {
		return ""
	}
	return strings.TrimSpace(string(src[start:end]))
}

func terminalCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	_, ok = terminalActionScore[sel.Sel.Name]
	return sel.Sel.Name, ok
}

func functionName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + exprName(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

func functionResults(fnType *ast.FuncType) (int, []errorSlot) {
	if fnType.Results == nil {
		return 0, nil
	}
	count := 0
	var slots []errorSlot
	for _, field := range fnType.Results.List {
		width := len(field.Names)
		if width == 0 {
			width = 1
		}
		id, isError := field.Type.(*ast.Ident)
		isError = isError && id.Name == "error"
		for i := 0; i < width; i++ {
			if isError {
				slot := errorSlot{Index: count}
				if len(field.Names) > i {
					slot.Name = field.Names[i].Name
					slot.Obj = field.Names[i].Obj
				}
				slots = append(slots, slot)
			}
			count++
		}
	}
	return count, slots
}

func namedErrorResults(slots []errorSlot) []string {
	var names []string
	for _, slot := range slots {
		if slot.Name != "" {
			names = append(names, slot.Name)
		}
	}
	sort.Strings(names)
	return names
}

func isActualResult(id *ast.Ident, slots []errorSlot) bool {
	if id == nil || id.Name == "_" {
		return false
	}
	for _, slot := range slots {
		if slot.Name == "" || id.Name != slot.Name {
			continue
		}
		if slot.Obj != nil && id.Obj != nil {
			return slot.Obj == id.Obj
		}
		// Object resolution can be absent for malformed or generated files. In
		// that case retain the candidate but mark the binding by name.
		return true
	}
	return false
}

func buildParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node)
	var stack []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

func containsNode(container, target ast.Node) bool {
	return container != nil && target != nil &&
		target.Pos() >= container.Pos() && target.End() <= container.End()
}

func sinkForCall(call *ast.CallExpr, parents map[ast.Node]ast.Node) (ast.Expr, string) {
	for node := ast.Node(call); node != nil; node = parents[node] {
		switch parent := parents[node].(type) {
		case *ast.AssignStmt:
			index := -1
			for i, rhs := range parent.Rhs {
				if containsNode(rhs, call) {
					index = i
					break
				}
			}
			if index < 0 || len(parent.Lhs) == 0 {
				continue
			}
			if len(parent.Rhs) == len(parent.Lhs) && index < len(parent.Lhs) {
				return parent.Lhs[index], "assignment"
			}
			return parent.Lhs[len(parent.Lhs)-1], "assignment"
		case *ast.ValueSpec:
			if len(parent.Names) > 0 {
				return parent.Names[len(parent.Names)-1], "declaration"
			}
		case *ast.ReturnStmt:
			return nil, "deferred_return"
		case *ast.ExprStmt:
			return nil, "ignored"
		}
	}
	return nil, "unbound_expression"
}

func identFromExpr(expr ast.Expr) *ast.Ident {
	switch x := expr.(type) {
	case *ast.Ident:
		return x
	case *ast.ParenExpr:
		return identFromExpr(x.X)
	}
	return nil
}

func usesIdent(node ast.Node, target *ast.Ident) bool {
	if node == nil || target == nil {
		return false
	}
	used := false
	ast.Inspect(node, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if target.Obj != nil && id.Obj != nil {
			used = target.Obj == id.Obj
		} else {
			used = target.Name == id.Name
		}
		return !used
	})
	return used
}

func flowsToActualResult(body ast.Node, call *ast.CallExpr, sink *ast.Ident, slots []errorSlot) bool {
	if sink == nil {
		return false
	}
	flow := false
	ast.Inspect(body, func(node ast.Node) bool {
		if flow || node == nil || node.Pos() <= call.Pos() {
			return !flow
		}
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		rhsUsesSink := false
		for _, rhs := range assign.Rhs {
			rhsUsesSink = rhsUsesSink || usesIdent(rhs, sink)
		}
		if !rhsUsesSink {
			return true
		}
		for _, lhs := range assign.Lhs {
			if isActualResult(identFromExpr(lhs), slots) {
				flow = true
				return false
			}
		}
		return true
	})
	return flow
}

func joinHandlerOwnsResult(call *ast.CallExpr, parents map[ast.Node]ast.Node, slots []errorSlot) bool {
	for node := ast.Node(call); node != nil; node = parents[node] {
		outer, ok := parents[node].(*ast.CallExpr)
		if !ok || outer == call {
			continue
		}
		name := exprName(outer.Fun)
		if !strings.HasSuffix(name, "AppendInto") && !strings.HasSuffix(name, "AppendInvoke") {
			continue
		}
		for _, arg := range outer.Args {
			unary, ok := arg.(*ast.UnaryExpr)
			if ok && unary.Op == token.AND && isActualResult(identFromExpr(unary.X), slots) {
				return true
			}
		}
	}
	return false
}

func nilErrorReturnLines(fset *token.FileSet, body *ast.BlockStmt, resultCount int, slots []errorSlot) []int {
	var lines []int
	if resultCount == 0 || len(slots) == 0 {
		return lines
	}
	ast.Inspect(body, func(node ast.Node) bool {
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		ret, ok := node.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != resultCount {
			return true
		}
		allNil := true
		for _, slot := range slots {
			id, ok := ret.Results[slot.Index].(*ast.Ident)
			allNil = allNil && ok && id.Name == "nil"
		}
		if allNil {
			lines = append(lines, fset.Position(ret.Pos()).Line)
		}
		return true
	})
	sort.Ints(lines)
	return lines
}

func functionDefers(body *ast.BlockStmt) []*ast.DeferStmt {
	var result []*ast.DeferStmt
	ast.Inspect(body, func(node ast.Node) bool {
		if node == nil {
			return true
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		if stmt, ok := node.(*ast.DeferStmt); ok {
			result = append(result, stmt)
		}
		return true
	})
	return result
}

func callsIn(node ast.Node) []string {
	set := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := exprName(call.Fun)
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

func classifyPropagation(call *ast.CallExpr, body ast.Node, parents map[ast.Node]ast.Node, slots []errorSlot) (ast.Expr, string, bool, string) {
	sinkExpr, use := sinkForCall(call, parents)
	sinkID := identFromExpr(sinkExpr)
	if len(slots) == 0 {
		return sinkExpr, "no_error_result", false, "enclosing function has no error result"
	}
	if sinkID != nil && sinkID.Name == "_" {
		return sinkExpr, "blank", false, "terminal result is assigned to the blank identifier"
	}
	if isActualResult(sinkID, slots) {
		return sinkExpr, "actual_result", true, "terminal result directly mutates the named error result"
	}
	if joinHandlerOwnsResult(call, parents, slots) {
		return sinkExpr, "actual_result_join", true, "a recognized join handler owns the named error result"
	}
	if sinkID != nil && flowsToActualResult(body, call, sinkID, slots) {
		return sinkExpr, "local_then_result", true, "the local terminal error flows into the named error result"
	}
	if sinkID != nil {
		return sinkExpr, "local", false, "terminal error remains in a local or shadowed binding"
	}
	return sinkExpr, use, false, "terminal error has no path to an actual named error result"
}

func scoreCandidate(action, sinkBinding, actionExpr string, propagated bool, slots []errorSlot, nilReturns []int) int {
	score := terminalActionScore[action]
	if len(slots) > 0 {
		score += 20
	} else {
		score -= 10
	}
	if !propagated {
		score += 35
	}
	if len(nilReturns) > 0 {
		score += 15
	}
	if sinkBinding == "local" || sinkBinding == "blank" || sinkBinding == "ignored" {
		score += 6
	}
	lower := strings.ToLower(actionExpr)
	for _, owner := range []string{"txn", "transaction", "engine", "writer", "store", "checkpoint", "meta"} {
		if strings.Contains(lower, owner) {
			score += 2
			break
		}
	}
	if score > 100 {
		return 100
	}
	return score
}

func scanCallable(
	fset *token.FileSet,
	src []byte,
	rel string,
	fnName string,
	rootKey string,
	fnLine int,
	fnType *ast.FuncType,
	body *ast.BlockStmt,
	closed map[string]struct{},
	includeClosed bool,
	out *[]candidate,
) {
	if body == nil {
		return
	}
	resultCount, slots := functionResults(fnType)
	nilReturns := nilErrorReturnLines(fset, body, resultCount, slots)
	_, isClosed := closed[rootKey]
	if isClosed && !includeClosed {
		return
	}

	for _, deferStmt := range functionDefers(body) {
		var body ast.Node = deferStmt
		var calls []*ast.CallExpr
		if action, ok := terminalCall(deferStmt.Call); ok {
			_ = action
			calls = append(calls, deferStmt.Call)
		} else if lit, ok := deferStmt.Call.Fun.(*ast.FuncLit); ok && lit.Body != nil {
			body = lit.Body
			ast.Inspect(lit.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if _, ok := terminalCall(call); ok {
					calls = append(calls, call)
				}
				return true
			})
		}
		if len(calls) == 0 {
			continue
		}
		parents := buildParents(body)
		handlers := callsIn(body)
		for _, call := range calls {
			action, _ := terminalCall(call)
			sink, sinkBinding, propagated, reason := classifyPropagation(call, body, parents, slots)
			actionExpr := sourceText(fset, src, call)
			*out = append(*out, candidate{
				Path: rel, Function: fnName,
				FunctionLine: fnLine,
				DeferLine:    fset.Position(deferStmt.Pos()).Line,
				ActionLine:   fset.Position(call.Pos()).Line,
				Action:       action, ActionExpr: actionExpr,
				ErrorResultCount: len(slots), NamedErrorResults: namedErrorResults(slots),
				Sink: sourceText(fset, src, sink), SinkBinding: sinkBinding,
				PublicErrorPropagation: propagated, PropagationReason: reason,
				NilErrorReturnLines: nilReturns, DeferredHandlingCalls: handlers,
				ClosedRoot: isClosed,
				Score:      scoreCandidate(action, sinkBinding, actionExpr, propagated, slots, nilReturns),
			})
		}
	}
}

func scanFunction(fset *token.FileSet, src []byte, rel string, fn *ast.FuncDecl, closed map[string]struct{}, includeClosed bool, out *[]candidate) {
	if fn.Body == nil {
		return
	}
	scanCallable(
		fset, src, rel, functionName(fn), rel+":"+fn.Name.Name,
		fset.Position(fn.Pos()).Line, fn.Type, fn.Body, closed, includeClosed, out,
	)
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, ok := node.(*ast.FuncLit)
		if !ok {
			return true
		}
		line := fset.Position(lit.Pos()).Line
		label := fn.Name.Name + "$lit@" + strconv.Itoa(line)
		scanCallable(
			fset, src, rel, functionName(fn)+"$lit@"+strconv.Itoa(line), rel+":"+label,
			line, lit.Type, lit.Body, closed, includeClosed, out,
		)
		return true
	})
}

func readClosedRoots(path string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	if path == "" {
		return result, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		result[line] = struct{}{}
	}
	return result, scanner.Err()
}

func main() {
	root := flag.String("root", ".", "Go source root")
	closedPath := flag.String("closed-roots", "", "newline-delimited path:function closed-root pack")
	includeClosed := flag.Bool("include-closed", false, "include closed roots as calibration hits")
	includePropagated := flag.Bool("include-propagated", false, "include actions whose error reaches the actual result")
	includeUnbound := flag.Bool("include-unbound", false, "include ignored, blank, and syntactically unbound terminal results")
	includeNoErrorResult := flag.Bool("include-no-error-result", false, "include functions with no error result slot")
	minScore := flag.Int("min-score", 0, "minimum source-shape score")
	flag.Parse()

	closed, err := readClosedRoots(*closedPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fset := token.NewFileSet()
	var found []candidate
	err = filepath.WalkDir(*root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(fset, path, src, 0)
		if parseErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(*root, path)
		if relErr != nil {
			rel = path
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				scanFunction(fset, src, filepath.ToSlash(rel), fn, closed, *includeClosed, &found)
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	filtered := found[:0]
	for _, item := range found {
		if item.Score < *minScore || (!*includePropagated && item.PublicErrorPropagation) {
			continue
		}
		if !*includeNoErrorResult && item.ErrorResultCount == 0 {
			continue
		}
		if !*includeUnbound && item.SinkBinding != "local" {
			continue
		}
		filtered = append(filtered, item)
	}
	found = filtered
	sort.Slice(found, func(i, j int) bool {
		if found[i].Score != found[j].Score {
			return found[i].Score > found[j].Score
		}
		if found[i].Path != found[j].Path {
			return found[i].Path < found[j].Path
		}
		return found[i].ActionLine < found[j].ActionLine
	})

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"count":                   len(found),
		"min_score":               *minScore,
		"closed_roots_loaded":     len(closed),
		"include_closed":          *includeClosed,
		"include_propagated":      *includePropagated,
		"include_unbound":         *includeUnbound,
		"include_no_error_result": *includeNoErrorResult,
		"source_root":             *root,
		"scanner_schema":          "s46-return-slot-v1",
		"scanner_version":         strconv.Itoa(1),
		"candidates":              found,
	}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
