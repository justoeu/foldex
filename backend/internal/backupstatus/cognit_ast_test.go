package backupstatus

import (
	"go/ast"
	"go/token"
)

// Cognitive visitor copied from gocognit v1.2.1 (test-only; no go.mod pin).
// Analyzer dropped: tests call cognitComplexity on a parsed FuncDecl.

func cognitFuncName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && fn.Recv.NumFields() > 0 {
		return "(" + cognitRecvString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
	}
	return fn.Name.Name
}

func cognitRecvString(recv ast.Expr) string {
	switch t := recv.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + cognitRecvString(t.X)
	case *ast.IndexExpr:
		return cognitRecvString(t.X)
	case *ast.IndexListExpr:
		return cognitRecvString(t.X)
	}
	return "BADRECV"
}

func cognitComplexity(fn *ast.FuncDecl) int {
	v := cognitVisitor{name: fn.Name}
	ast.Walk(&v, fn)
	return v.complexity
}

type cognitVisitor struct {
	name            *ast.Ident
	complexity      int
	nesting         int
	elseNodes       map[ast.Node]bool
	calculatedExprs map[ast.Expr]bool
}

func (v *cognitVisitor) incNesting() { v.nesting++ }
func (v *cognitVisitor) decNesting() { v.nesting-- }

func (v *cognitVisitor) incComplexity() { v.complexity++ }

func (v *cognitVisitor) nestIncComplexity() { v.complexity += v.nesting + 1 }

func (v *cognitVisitor) markAsElseNode(n ast.Node) {
	if v.elseNodes == nil {
		v.elseNodes = make(map[ast.Node]bool)
	}
	v.elseNodes[n] = true
}

func (v *cognitVisitor) markedAsElseNode(n ast.Node) bool {
	return v.elseNodes[n]
}

func (v *cognitVisitor) markCalculated(e ast.Expr) {
	if v.calculatedExprs == nil {
		v.calculatedExprs = make(map[ast.Expr]bool)
	}
	v.calculatedExprs[e] = true
}

func (v *cognitVisitor) isCalculated(e ast.Expr) bool {
	return v.calculatedExprs[e]
}

func (v *cognitVisitor) Visit(n ast.Node) ast.Visitor {
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

func (v *cognitVisitor) visitIfStmt(n *ast.IfStmt) ast.Visitor {
	v.incIfComplexity(n)
	if n.Init != nil {
		ast.Walk(v, n.Init)
	}
	ast.Walk(v, n.Cond)
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	if _, ok := n.Else.(*ast.BlockStmt); ok {
		v.incComplexity()
		ast.Walk(v, n.Else)
	} else if _, ok := n.Else.(*ast.IfStmt); ok {
		v.markAsElseNode(n.Else)
		ast.Walk(v, n.Else)
	}
	return nil
}

func (v *cognitVisitor) visitSwitchStmt(n *ast.SwitchStmt) ast.Visitor {
	v.nestIncComplexity()
	if n.Init != nil {
		ast.Walk(v, n.Init)
	}
	if n.Tag != nil {
		ast.Walk(v, n.Tag)
	}
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitTypeSwitchStmt(n *ast.TypeSwitchStmt) ast.Visitor {
	v.nestIncComplexity()
	if n.Init != nil {
		ast.Walk(v, n.Init)
	}
	if n.Assign != nil {
		ast.Walk(v, n.Assign)
	}
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitSelectStmt(n *ast.SelectStmt) ast.Visitor {
	v.nestIncComplexity()
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitForStmt(n *ast.ForStmt) ast.Visitor {
	v.nestIncComplexity()
	if n.Init != nil {
		ast.Walk(v, n.Init)
	}
	if n.Cond != nil {
		ast.Walk(v, n.Cond)
	}
	if n.Post != nil {
		ast.Walk(v, n.Post)
	}
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitRangeStmt(n *ast.RangeStmt) ast.Visitor {
	v.nestIncComplexity()
	if n.Key != nil {
		ast.Walk(v, n.Key)
	}
	if n.Value != nil {
		ast.Walk(v, n.Value)
	}
	ast.Walk(v, n.X)
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitFuncLit(n *ast.FuncLit) ast.Visitor {
	ast.Walk(v, n.Type)
	v.incNesting()
	ast.Walk(v, n.Body)
	v.decNesting()
	return nil
}

func (v *cognitVisitor) visitBranchStmt(n *ast.BranchStmt) ast.Visitor {
	if n.Label != nil {
		v.incComplexity()
	}
	return v
}

func (v *cognitVisitor) visitBinaryExpr(n *ast.BinaryExpr) ast.Visitor {
	if isCognitLogicalOp(n.Op) && !v.isCalculated(n) {
		ops := v.collectBinaryOps(n)
		var lastOp token.Token
		for _, op := range ops {
			if lastOp != op {
				v.incComplexity()
				lastOp = op
			}
		}
	}
	return v
}

func (v *cognitVisitor) visitCallExpr(n *ast.CallExpr) ast.Visitor {
	if callIdent, ok := n.Fun.(*ast.Ident); ok {
		if callIdent.Obj == v.name.Obj && callIdent.Name == v.name.Name {
			v.incComplexity()
		}
	}
	return v
}

func (v *cognitVisitor) collectBinaryOps(exp ast.Expr) []token.Token {
	v.markCalculated(exp)
	if exp, ok := exp.(*ast.BinaryExpr); ok {
		return mergeCognitBinaryOps(v.collectBinaryOps(exp.X), exp.Op, v.collectBinaryOps(exp.Y))
	}
	return nil
}

func (v *cognitVisitor) incIfComplexity(n *ast.IfStmt) {
	if v.markedAsElseNode(n) {
		v.incComplexity()
	} else {
		v.nestIncComplexity()
	}
}

func mergeCognitBinaryOps(x []token.Token, op token.Token, y []token.Token) []token.Token {
	out := append([]token.Token{}, x...)
	if isCognitLogicalOp(op) {
		out = append(out, op)
	}
	return append(out, y...)
}

func isCognitLogicalOp(op token.Token) bool {
	return op == token.LAND || op == token.LOR
}
