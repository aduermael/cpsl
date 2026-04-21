// docstring.go implements the docstring rule: every non-test Go source file
// must have a leading doc comment of at least minDocstringChars characters
// and at most maxDocstringLines lines.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"strings"
)

func runDocstring(args []string) ([]Violation, error) {
	fs := flag.NewFlagSet("docstring", flag.ExitOnError)
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
		if file.Doc == nil {
			violations = append(violations, Violation{
				Path:    path,
				Line:    1,
				Rule:    "docstring",
				Message: "missing file doc comment before package declaration",
			})
			return nil
		}
		body := strings.TrimRight(file.Doc.Text(), "\n")
		chars := len(body)
		if chars < minDocstringChars {
			violations = append(violations, Violation{
				Path:    path,
				Line:    fset.Position(file.Doc.Pos()).Line,
				Rule:    "docstring",
				Message: fmt.Sprintf("file doc comment is %d chars (min %d)", chars, minDocstringChars),
			})
		}
		lines := strings.Count(body, "\n") + 1
		if body == "" {
			lines = 0
		}
		if lines > maxDocstringLines {
			violations = append(violations, Violation{
				Path:    path,
				Line:    fset.Position(file.Doc.Pos()).Line,
				Rule:    "docstring",
				Message: fmt.Sprintf("file doc comment is %d lines (max %d)", lines, maxDocstringLines),
			})
		}
		return nil
	})
	return violations, err
}
