// positional.go implements the positional-params rule: functions and
// methods must not exceed maxPositionalParams positional params. A leading
// context.Context is exempt; a final variadic param is allowed.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
)

func runPositionalParams(args []string) ([]Violation, error) {
	fs := flag.NewFlagSet("positional-params", flag.ExitOnError)
	maxParams := fs.Int("max-params", maxPositionalParams, "maximum positional parameters per function")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	paths := fs.Args()

	var violations []Violation
	fset := token.NewFileSet()
	err := walkGoFiles(paths, func(path string) error {
		file, err := parseGoFile(fset, path)
		if err != nil || file == nil {
			return nil
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			count := countPositionalParams(fn.Type.Params)
			if count > *maxParams {
				pos := fset.Position(fn.Pos())
				violations = append(violations, Violation{
					Path:    path,
					Line:    pos.Line,
					Rule:    "positional-params",
					Message: fmt.Sprintf("function %s has %d positional params (max %d)", funcName(fn), count, *maxParams),
				})
			}
		}
		return nil
	})
	return violations, err
}

// countPositionalParams returns the number of positional parameters in the
// given list, excluding a leading context.Context and any final variadic
// parameter.
func countPositionalParams(params *ast.FieldList) int {
	if params == nil {
		return 0
	}
	types := flattenFieldTypes(params)
	if len(types) == 0 {
		return 0
	}
	start := 0
	if isContextType(types[0]) {
		start = 1
	}
	count := 0
	for i := start; i < len(types); i++ {
		if _, ok := types[i].(*ast.Ellipsis); ok {
			continue
		}
		count++
	}
	return count
}

// flattenFieldTypes expands each named group (e.g., "a, b int") into one
// entry per name, so each positional slot is represented once.
func flattenFieldTypes(fl *ast.FieldList) []ast.Expr {
	var out []ast.Expr
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			out = append(out, f.Type)
			continue
		}
		for range f.Names {
			out = append(out, f.Type)
		}
	}
	return out
}

func isContextType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "context" && sel.Sel.Name == "Context"
}

func funcName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "(...)." + fn.Name.Name
	}
	return fn.Name.Name
}
