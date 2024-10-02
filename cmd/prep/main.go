package main

import (
	"bytes"
	"fmt"
	"go/build"
	"go/token"
	"math"
	"strings"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/dave/dst/dstutil"
	"github.com/pijng/goinject"
	"github.com/pijng/yaegi/interp"
	"github.com/pijng/yaegi/stdlib"
)

const PrepPrefix = "prep_"
const PrepPath = "github.com/pijng/prep"
const ComptimeName = "Comptime"
const FuncsPath = "funcs"
const VarsPath = "vars"

type ComptimeModifier struct {
	intr *interp.Interpreter
}

func main() {
	intr := interp.New(interp.Options{GoPath: build.Default.GOPATH})
	err := intr.Use(stdlib.Symbols)
	if err != nil {
		panic(err)
	}

	cmpm := ComptimeModifier{intr: intr}

	goinject.Process(&cmpm)
}

func (cmpm *ComptimeModifier) Modify(f *dst.File, dec *decorator.Decorator, res *decorator.Restorer) *dst.File {
	newFuncs := collectFuncs(f, res)
	existingFuncs := Restore(FuncsPath)
	funcs := Merge(existingFuncs, newFuncs)
	Dump(funcs, FuncsPath)

	globalScope := collectVars(f)
	currentScope := globalScope

	dstutil.Apply(f, func(c *dstutil.Cursor) bool {
		switch c.Node().(type) {
		case *dst.FuncDecl:
			currentScope = &Scope{vars: make(map[string]string), parent: currentScope}
		case *dst.BlockStmt:
			currentScope = &Scope{vars: make(map[string]string), parent: currentScope}
		}
		callExpr, ok := c.Node().(*dst.CallExpr)
		if !ok {
			return true
		}

		funcIdent, isIdent := callExpr.Fun.(*dst.Ident)
		if !isIdent || funcIdent.Name != ComptimeName || funcIdent.Path != PrepPath {
			return true
		}

		argExpr, isExpr := callExpr.Args[0].(*dst.CallExpr)
		if !isExpr {
			return true
		}

		funcToCallIdent, isIdent := argExpr.Fun.(*dst.Ident)
		if !isIdent {
			return true
		}

		funcToCall := funcToCallIdent.Name

		args := make([]string, len(argExpr.Args))
		for idx, arg := range argExpr.Args {
			switch expr := arg.(type) {
			case *dst.Ident:
				value, found := lookupVariable(expr.Name, currentScope)
				if !found {
					panic(fmt.Sprintf("cannot find variable '%s' in scope", expr.Name))
				}
				args[idx] = value
			case *dst.BasicLit:
				args[idx] = expr.Value
			default:
				panic(fmt.Sprintf("cannot use '%T' as argument to function call at comptime", expr))
			}
		}
		argsStr := strings.Join(args, ", ")

		fn, ok := funcs[funcToCall]
		if !ok {
			panic(fmt.Sprintf("Function '%s' not found", funcToCall))
		}

		_, err := cmpm.intr.Eval(fn)
		if err != nil {
			panic(fmt.Sprintf("Cannot evaluate function '%s': %v", funcToCall, err))
		}

		call := fmt.Sprintf("%s%s.%s(%v)", PrepPrefix, funcToCall, funcToCall, argsStr)
		res, err := cmpm.intr.Eval(call)
		if err != nil {
			panic(fmt.Sprintf("Cannot call function '%s': %v", funcToCall, err))
		}

		typeName := strings.ToUpper(res.Type().Name())
		if typeName == "" {
			panic(fmt.Sprintf("cannot use '%s' as return type of '%s(%v)' call at comptime", res.Type(), funcToCall, argsStr))
		}

		tokenValue := token.Lookup(typeName)
		lit := &dst.BasicLit{
			Kind:  tokenValue,
			Value: fmt.Sprintf("%q", res.Interface()),
		}

		c.Replace(lit)

		return true
	}, func(c *dstutil.Cursor) bool {
		switch c.Node().(type) {
		case *dst.FuncDecl, *dst.BlockStmt:
			currentScope = currentScope.parent
		}
		return true
	})

	return f
}

func lookupVariable(name string, scope *Scope) (string, bool) {
	for s := scope; s != nil; s = s.parent {
		if val, ok := s.vars[name]; ok {
			return val, true
		}
	}
	return "", false
}

func collectFuncs(f *dst.File, res *decorator.Restorer) map[string]string {
	funcs := make(map[string]string)

	for _, decl := range f.Decls {
		decl, isFunc := decl.(*dst.FuncDecl)
		if !isFunc {
			continue
		}

		var buf bytes.Buffer
		clonedDecl := dst.Clone(decl).(*dst.FuncDecl)
		funcF := &dst.File{
			Name:  dst.NewIdent(fmt.Sprintf("%s%s", PrepPrefix, clonedDecl.Name)),
			Decls: []dst.Decl{clonedDecl},
		}

		err := res.Fprint(&buf, funcF)
		if err != nil {
			panic(err)
		}

		funcs[decl.Name.Name] = buf.String()
	}

	return funcs
}

type Scope struct {
	parent *Scope
	vars   map[string]string
}

func collectVars(f *dst.File) *Scope {
	global := &Scope{vars: make(map[string]string)}
	current := global

	dstutil.Apply(f, func(c *dstutil.Cursor) bool {
		switch node := c.Node().(type) {
		case *dst.FuncDecl:
			current = &Scope{parent: current, vars: make(map[string]string)}
		case *dst.BlockStmt:
			current = &Scope{parent: current, vars: make(map[string]string)}
		case *dst.AssignStmt:
			handleAssignment(node, current)
		}
		return true
	}, func(c *dstutil.Cursor) bool {
		switch c.Node().(type) {
		case *dst.FuncDecl, *dst.BlockStmt:
			current = current.parent
		}
		return true
	})
	return global
}

func handleAssignment(assignStmt *dst.AssignStmt, scope *Scope) {
	for idx, lhs := range assignStmt.Lhs {
		ident, ok := lhs.(*dst.Ident)
		if !ok {
			continue
		}
		var rhs dst.Expr
		if len(assignStmt.Lhs) != len(assignStmt.Rhs) {
			op := float64(len(assignStmt.Rhs)) / float64(len(assignStmt.Lhs))
			idx = int(math.Floor(op))
		}

		if len(assignStmt.Rhs) == 1 && len(assignStmt.Lhs) > 1 {
			rhs = assignStmt.Rhs[0]
		} else {
			rhs = assignStmt.Rhs[idx]
		}
		lit, ok := rhs.(*dst.BasicLit)
		if !ok {
			continue
		}
		scope.vars[ident.Name] = lit.Value
	}
}
