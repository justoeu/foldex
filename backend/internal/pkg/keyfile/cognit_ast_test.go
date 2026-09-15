package keyfile

import (
	"go/ast"
	"go/token"
	"testing"

	"foldex/internal/testsupport"
)

// recvString returns a string representation of recv of the
// form "T", "*T", or "BADRECV" (if not a proper receiver type).
func recvString(recv ast.Expr) string {
	switch t := recv.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + recvString(t.X)
	case *ast.IndexExpr:
		return recvString(t.X)
	case *ast.IndexListExpr:
		return recvString(t.X)
	}

	return "BADRECV"
}

// Complexity calculates the cognitive complexity of a function.
func Complexity(fn *ast.FuncDecl) int {
	res := ScanComplexity(fn, false)

	return res.Complexity
}

// ScanComplexity scans the function declaration.
func ScanComplexity(fn *ast.FuncDecl, includeDiagnostics bool) ScanResult {
	v := complexityVisitor{
		name:               fn.Name,
		diagnosticsEnabled: includeDiagnostics,
	}

	ast.Walk(&v, fn)

	return ScanResult{
		Diagnostics: v.diagnostics,
		Complexity:  v.complexity,
	}
}

type ScanResult struct {
	Diagnostics []diagnostic
	Complexity  int
}

type diagnostic struct {
	Inc     int
	Nesting int
	Text    string
	Pos     token.Pos
}

type complexityVisitor struct {
	name            *ast.Ident
	complexity      int
	nesting         int
	elseNodes       map[ast.Node]bool
	calculatedExprs map[ast.Expr]bool

	diagnosticsEnabled bool
	diagnostics        []diagnostic
}

func (v *complexityVisitor) incNesting() {
	v.nesting++
}

func (v *complexityVisitor) decNesting() {
	v.nesting--
}

func (v *complexityVisitor) incComplexity(text string, pos token.Pos) {
	v.complexity++

	if !v.diagnosticsEnabled {
		return
	}

	v.diagnostics = append(v.diagnostics, diagnostic{
		Inc:  1,
		Text: text,
		Pos:  pos,
	})
}

func (v *complexityVisitor) nestIncComplexity(text string, pos token.Pos) {
	v.complexity += (v.nesting + 1)

	if !v.diagnosticsEnabled {
		return
	}

	v.diagnostics = append(v.diagnostics, diagnostic{
		Inc:     v.nesting + 1,
		Nesting: v.nesting,
		Text:    text,
		Pos:     pos,
	})
}

func (v *complexityVisitor) markAsElseNode(n ast.Node) {
	if v.elseNodes == nil {
		v.elseNodes = make(map[ast.Node]bool)
	}

	v.elseNodes[n] = true
}

func (v *complexityVisitor) markedAsElseNode(n ast.Node) bool {
	if v.elseNodes == nil {
		return false
	}

	return v.elseNodes[n]
}

func (v *complexityVisitor) markCalculated(e ast.Expr) {
	if v.calculatedExprs == nil {
		v.calculatedExprs = make(map[ast.Expr]bool)
	}

	v.calculatedExprs[e] = true
}

func (v *complexityVisitor) isCalculated(e ast.Expr) bool {
	if v.calculatedExprs == nil {
		return false
	}

	return v.calculatedExprs[e]
}

// Visit implements the ast.Visitor interface.
func (v *complexityVisitor) Visit(n ast.Node) ast.Visitor {
	switch n := n.(type) {
	case *ast.IfStmt:
		return v.visitIfStmt(n)
	case *ast.SwitchStmt:
		return v.visitSwitchStmt(n)
	case *ast.TypeSwitchStmt:
		return v.visitTypeSwitchStmt(n)
	case *ast.SelectStmt:
		return v.visitSelectStmt(n)
	case *ast.ForStmt:
		return v.visitForStmt(n)
	case *ast.RangeStmt:
		return v.visitRangeStmt(n)
	case *ast.FuncLit:
		return v.visitFuncLit(n)
	case *ast.BranchStmt:
		return v.visitBranchStmt(n)
	case *ast.BinaryExpr:
		return v.visitBinaryExpr(n)
	case *ast.CallExpr:
		return v.visitCallExpr(n)
	}

	return v
}

func (v *complexityVisitor) visitIfStmt(n *ast.IfStmt) ast.Visitor {
	v.incIfComplexity(n, "if", n.Pos())

	if n := n.Init; n != nil {
		ast.Walk(v, n)
	}

	ast.Walk(v, n.Cond)

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	if _, ok := n.Else.(*ast.BlockStmt); ok {
		v.incComplexity("else", n.Else.Pos())

		ast.Walk(v, n.Else)
	} else if _, ok := n.Else.(*ast.IfStmt); ok {
		v.markAsElseNode(n.Else)
		ast.Walk(v, n.Else)
	}

	return nil
}

func (v *complexityVisitor) visitSwitchStmt(n *ast.SwitchStmt) ast.Visitor {
	v.nestIncComplexity("switch", n.Pos())

	if n := n.Init; n != nil {
		ast.Walk(v, n)
	}

	if n := n.Tag; n != nil {
		ast.Walk(v, n)
	}

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitTypeSwitchStmt(n *ast.TypeSwitchStmt) ast.Visitor {
	v.nestIncComplexity("switch", n.Pos())

	if n := n.Init; n != nil {
		ast.Walk(v, n)
	}

	if n := n.Assign; n != nil {
		ast.Walk(v, n)
	}

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitSelectStmt(n *ast.SelectStmt) ast.Visitor {
	v.nestIncComplexity("select", n.Pos())

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitForStmt(n *ast.ForStmt) ast.Visitor {
	v.nestIncComplexity("for", n.Pos())

	if n := n.Init; n != nil {
		ast.Walk(v, n)
	}

	if n := n.Cond; n != nil {
		ast.Walk(v, n)
	}

	if n := n.Post; n != nil {
		ast.Walk(v, n)
	}

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitRangeStmt(n *ast.RangeStmt) ast.Visitor {
	v.nestIncComplexity("for", n.Pos())

	if n := n.Key; n != nil {
		ast.Walk(v, n)
	}

	if n := n.Value; n != nil {
		ast.Walk(v, n)
	}

	ast.Walk(v, n.X)

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitFuncLit(n *ast.FuncLit) ast.Visitor {
	ast.Walk(v, n.Type)

	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()

	return nil
}

func (v *complexityVisitor) visitBranchStmt(n *ast.BranchStmt) ast.Visitor {
	if n.Label != nil {
		v.incComplexity(n.Tok.String(), n.Pos())
	}

	return v
}

func (v *complexityVisitor) visitBinaryExpr(n *ast.BinaryExpr) ast.Visitor {
	if isBinaryLogicalOp(n.Op) && !v.isCalculated(n) {
		ops := v.collectBinaryOps(n)

		var lastOp token.Token
		for _, op := range ops {
			if lastOp != op {
				v.incComplexity(op.String(), n.OpPos)
				lastOp = op
			}
		}
	}

	return v
}

func (v *complexityVisitor) visitCallExpr(n *ast.CallExpr) ast.Visitor {
	if callIdent, ok := n.Fun.(*ast.Ident); ok {
		obj, name := callIdent.Obj, callIdent.Name
		if obj == v.name.Obj && name == v.name.Name {
			// called by same function directly (direct recursion)
			v.incComplexity(name, n.Pos())
		}
	}

	return v
}

func (v *complexityVisitor) collectBinaryOps(exp ast.Expr) []token.Token {
	v.markCalculated(exp)

	if exp, ok := exp.(*ast.BinaryExpr); ok {
		return mergeBinaryOps(v.collectBinaryOps(exp.X), exp.Op, v.collectBinaryOps(exp.Y))
	}
	return nil
}

func (v *complexityVisitor) incIfComplexity(n *ast.IfStmt, text string, pos token.Pos) {
	if v.markedAsElseNode(n) {
		v.incComplexity(text, pos)
	} else {
		v.nestIncComplexity(text, pos)
	}
}

func mergeBinaryOps(x []token.Token, op token.Token, y []token.Token) []token.Token {
	var out []token.Token
	out = append(out, x...)

	if isBinaryLogicalOp(op) {
		out = append(out, op)
	}

	out = append(out, y...)
	return out
}

func isBinaryLogicalOp(op token.Token) bool {
	return op == token.LAND || op == token.LOR
}

func cognitScores(t *testing.T) map[string]int {
	t.Helper()
	out := make(map[string]int)
	for _, fn := range testsupport.ProductionFuncs(t) {
		out[cognitFuncName(fn)] = Complexity(fn)
	}
	return out
}

func cognitFuncName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && fn.Recv.NumFields() > 0 {
		return "(" + recvString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

func assertCognitAtMost(t *testing.T, scores map[string]int, name string, max int) {
	t.Helper()
	got, ok := scores[name]
	if !ok {
		t.Fatalf("function %s not found in production AST", name)
	}
	if got > max {
		t.Fatalf("cognitive complexity of %s is %d, want <= %d", name, got, max)
	}
}
