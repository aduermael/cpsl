// report.go defines the Violation type and the stable reporter used to
// emit findings as path:line:[rule] message to stdout.
package main

import (
	"fmt"
	"io"
	"sort"
)

// Violation represents a single rule violation in a source file.
type Violation struct {
	Path    string
	Line    int
	Rule    string
	Message string
}

func reportViolations(w io.Writer, violations []Violation) {
	sorted := make([]Violation, len(violations))
	copy(sorted, violations)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
	for _, v := range sorted {
		line := v.Line
		if line < 1 {
			line = 1
		}
		fmt.Fprintf(w, "%s:%d: [%s] %s\n", v.Path, line, v.Rule, v.Message)
	}
}
