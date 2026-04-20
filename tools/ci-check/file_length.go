// file_length.go implements the file-length rule: every non-test Go source
// file must be at most max-lines lines (default 1000).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
)

func runFileLength(args []string) ([]Violation, error) {
	fs := flag.NewFlagSet("file-length", flag.ExitOnError)
	maxLines := fs.Int("max-lines", defaultMaxFileLines, "maximum lines per file")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	paths := fs.Args()

	var violations []Violation
	err := walkGoFiles(paths, func(path string) error {
		n, err := countLines(path)
		if err != nil {
			return err
		}
		if n > *maxLines {
			violations = append(violations, Violation{
				Path:    path,
				Line:    1,
				Rule:    "file-length",
				Message: fmt.Sprintf("file has %d lines (max %d)", n, *maxLines),
			})
		}
		return nil
	})
	return violations, err
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n := 0
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for s.Scan() {
		n++
	}
	return n, s.Err()
}
