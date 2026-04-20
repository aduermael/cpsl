// main.go dispatches ci-check subcommands (file-length, docstring,
// positional-params, all) and emits any rule violations to stdout.
package main

import (
	"fmt"
	"os"
)

const (
	defaultMaxFileLines = 1000
	minDocstringChars   = 60
	maxDocstringLines   = 3
	maxPositionalParams = 1
)

type ruleFunc func(args []string) ([]Violation, error)

var rules = map[string]ruleFunc{
	"file-length":       runFileLength,
	"docstring":         runDocstring,
	"positional-params": runPositionalParams,
}

var allRuleOrder = []string{"file-length", "docstring", "positional-params"}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	rule := os.Args[1]
	args := os.Args[2:]

	if rule == "all" {
		var all []Violation
		for _, name := range allRuleOrder {
			v, err := rules[name](args)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(2)
			}
			all = append(all, v...)
		}
		finish(all)
		return
	}

	fn, ok := rules[rule]
	if !ok {
		usage()
		os.Exit(2)
	}
	v, err := fn(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
	finish(v)
}

func finish(v []Violation) {
	if len(v) == 0 {
		return
	}
	reportViolations(os.Stdout, v)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ci-check <file-length|docstring|positional-params|all> [flags] [paths...]")
}
