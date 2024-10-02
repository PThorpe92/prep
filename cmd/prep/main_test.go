package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dave/dst/decorator"
)

func TestCollectFuncs(t *testing.T) {
	src := `
package main

func add(a int, b int) int {
    return a + b
}

func greet(name string) string {
    return "Hello, " + name
}
`
	f, err := decorator.Parse(src)
	if err != nil {
		t.Fatalf("Failed to parse source: %v", err)
	}
	res := decorator.NewRestorer()
	funcs := collectFuncs(f, res)

	expectedFuncs := map[string]string{
		"add": `package prep_add

func add(a int, b int) int {
	return a + b
}
`,
		"greet": `package prep_greet

func greet(name string) string {
	return "Hello, " + name
}
`,
	}

	for name, code := range expectedFuncs {
		collectedCode, ok := funcs[name]
		if !ok {
			t.Errorf("Function %s not found in collected functions", name)
			continue
		}
		if collectedCode != code {
			t.Errorf("Function %s code mismatch.\nExpected:\n%s\nGot:\n%s", name, code, collectedCode)
		}
	}
}

type scopedVar struct {
	scope       *Scope
	varName     string
	expectedVal string
	found       bool
}

func TestCollectGlobalVars(t *testing.T) {
	src := `
package main

var globalVar = "global"
const globalConst = 10
`

	f, err := decorator.Parse(src)
	if err != nil {
		t.Fatalf("Failed to parse source: %v", err)
	}
	scope := collectVars(f)

	tests := []struct {
		scope       *Scope
		varName     string
		expectedVal string
		found       bool
	}{
		{scope, "globalVar", `"global"`, true},
		{scope, "globalConst", `10`, true},
	}

	for idx := range tests {
		val, found := lookupVariable(tests[idx].varName, tests[idx].scope)
		if found != tests[idx].found {
			t.Errorf("Variable %s found: %v, expected: %v", tests[idx].varName, found, tests[idx].found)
			continue
		}
		if found && val != tests[idx].expectedVal {
			t.Errorf("Variable %s value mismatch. Got: %s, Expected: %s", tests[idx].varName, val, tests[idx].expectedVal)
		}
	}
}

func TestCollectScopedVars(t *testing.T) {
	src := `
package main

const globalVar = "global"

func main() {
   var localVar = "local"
   x := 10
   y := 20
}`
	f, err := decorator.Parse(src)
	if err != nil {
		t.Fatalf("Failed to parse source: %v", err)
	}
	scope := collectVars(f)
	if scope == nil || len(scope.children) != 1 {
		t.Errorf("Expected 1 child scope, got %d", len(scope.children))
	}

	mainScope := scope.children[0]

	tests := []scopedVar{
		{scope, "globalVar", `"global"`, true},
		{mainScope, "localVar", `"local"`, true},
		{mainScope, "x", `10`, true},
		{mainScope, "y", `20`, true},
	}
	printScope(scope, 0)

	for _, tt := range tests {
		val, found := lookupVariable(tt.varName, tt.scope)
		if found != tt.found {
			t.Errorf("Variable %s found: %v, expected: %v", tt.varName, found, tt.found)
			continue
		}
		if found && val != tt.expectedVal {
			t.Errorf("Variable %s value mismatch. Got: %s, Expected: %s", tt.varName, val, tt.expectedVal)
		}
	}
}

func TestCollectMultipleScopes(t *testing.T) {
	src := `
package main


func main() {
   var localVar = "local"
   x := 10
   y := 20
 inner := func() {
	innerX := 30
	innerY := 40
	x = 30
	y = 40
   }
   inner()
  }

const globalVar = "global"

func foo(a, b int) int {
   x := 10
   y := 20
   return x + y + a + b
}
`
	f, err := decorator.Parse(src)
	if err != nil {
		t.Fatalf("Failed to parse source: %v", err)
	}
	scope := collectVars(f)
	if scope == nil {
		t.Fatalf("Failed to collect vars")
	}
	if len(scope.children) <= 1 {
		t.Errorf("Expected 2 child scopes, got %d", len(scope.children))
	}
	mainScope := scope.children[0]
	if len(mainScope.children) != 1 {
		t.Fatalf("Expected 1 child scope, got %d", len(mainScope.children))
	}
	innerMainScope := mainScope.children[0]

	fooScope := scope.children[1]

	tests := []scopedVar{
		{mainScope, "x", `10`, true},
		{mainScope, "localVar", `"local"`, true},
		{mainScope, "y", `20`, true},
		{innerMainScope, "innerX", `30`, true},
		{innerMainScope, "innerY", `40`, true},
		{innerMainScope, "x", `30`, true},
		{scope, "globalVar", `"global"`, true},
		{fooScope, "x", `10`, true},
		{fooScope, "y", `20`, true},
	}
	printScope(scope, 0)

	for _, tt := range tests {
		val, found := lookupVariable(tt.varName, tt.scope)
		if found != tt.found {
			t.Errorf("Variable %s found: %v, expected: %v", tt.varName, found, tt.found)
			continue
		}
		if found && val != tt.expectedVal {
			t.Errorf("Variable %s value mismatch. Got: %s, Expected: %s", tt.varName, val, tt.expectedVal)
		}
	}
}

func printScope(scope *Scope, level int) {
	indent := strings.Repeat("  ", level)
	fmt.Printf("%sScope level %d:\n", indent, level)
	for name, val := range scope.vars {
		fmt.Printf("%s  Variable '%s' = %s\n", indent, name, val)
	}
	for _, child := range scope.children {
		printScope(child, level+1)
	}
}
