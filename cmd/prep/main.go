package main

import (
	"bytes"
	"fmt"
	"go/build"
	"go/token"
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
	parent   *Scope
	children []*Scope
	vars     map[string]string
}

func collectVars(f *dst.File) *Scope {
	global := &Scope{vars: make(map[string]string)}
	current := global

	dstutil.Apply(f, func(c *dstutil.Cursor) bool {
		switch node := c.Node().(type) {
		case *dst.FuncDecl:
			funcScope := &Scope{parent: current, vars: make(map[string]string)}
			current.children = append(current.children, funcScope)
			current = funcScope
		case *dst.BlockStmt:
			if _, isFuncBody := c.Parent().(*dst.FuncDecl); isFuncBody {
				// Do not create a new scope for the function body block
			} else {
				blockScope := &Scope{parent: current, vars: make(map[string]string)}
				current.children = append(current.children, blockScope)
				current = blockScope
			}
		case *dst.DeclStmt:
			if genDecl, ok := node.Decl.(*dst.GenDecl); ok {
				if genDecl.Tok == token.VAR || genDecl.Tok == token.CONST {
					handleGenDecl(genDecl, current)
				}
			}
		case *dst.GenDecl:
			if node.Tok == token.VAR || node.Tok == token.CONST {
				handleGenDecl(node, current)
			}
		case *dst.AssignStmt:
			handleAssignment(node, current)
		}
		return true
	}, func(c *dstutil.Cursor) bool {
		switch c.Node().(type) {
		case *dst.FuncDecl:
			current = current.parent
		case *dst.BlockStmt:
			if _, isFuncBody := c.Parent().(*dst.FuncDecl); isFuncBody {
				// didn't push, so don't pop
			} else {
				current = current.parent
			}
		}
		return true
	})
	return global
}

func handleGenDecl(genDecl *dst.GenDecl, scope *Scope) {
	for _, spec := range genDecl.Specs {
		valueSpec, ok := spec.(*dst.ValueSpec)
		if !ok {
			continue
		}
		for idx, name := range valueSpec.Names {
			if name.Name == "_" {
				continue
			}
			var val string
			if len(valueSpec.Values) > idx {
				switch expr := valueSpec.Values[idx].(type) {
				case *dst.BasicLit:
					val = expr.Value
				case *dst.Ident:
					if identVal, ok := lookupVariable(expr.Name, scope); ok {
						val = identVal
					}
				}
			}
			scope.vars[name.Name] = val
		}
	}
}

func handleAssignment(assignStmt *dst.AssignStmt, scope *Scope) {
	for idx, lhs := range assignStmt.Lhs {
		ident, ok := lhs.(*dst.Ident)
		if !ok {
			continue
		}
		var rhs dst.Expr
		if len(assignStmt.Rhs) == 1 && len(assignStmt.Lhs) > 1 {
			rhs = assignStmt.Rhs[0]
		} else {
			if idx >= len(assignStmt.Rhs) {
				continue
			}
			rhs = assignStmt.Rhs[idx]
		}
		switch expr := rhs.(type) {
		case *dst.BasicLit:
			if assignStmt.Tok == token.DEFINE {
				scope.vars[ident.Name] = expr.Value
			} else if assignStmt.Tok == token.ASSIGN {
				if _, found := lookupVariable(ident.Name, scope); found {
					scope.vars[ident.Name] = expr.Value
				}
			}
		case *dst.Ident:
			if val, found := lookupVariable(expr.Name, scope); found {
				scope.vars[ident.Name] = val
			}
		}
	}
}
