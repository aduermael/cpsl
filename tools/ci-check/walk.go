// walk.go provides the shared file walker and AST parse helper used by
// every rule. Honors the exclusion set (test files, external-deps, vendor,
// and ci-check itself).
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

var skipDirNames = []string{
	"external-deps-workspace",
	"vendor",
	"node_modules",
	".git",
}

var skipRelPaths = []string{
	"tools/ci-check",
}

// walkGoFiles walks each root path and invokes yield for every non-test
// .go source file, skipping excluded directories.
func walkGoFiles(roots []string, yield func(path string) error) error {
	if len(roots) == 0 {
		roots = []string{"."}
	}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(path)
			rel = strings.TrimPrefix(rel, "./")
			if d.IsDir() {
				if shouldSkipDir(rel, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return yield(path)
		}); err != nil {
			return err
		}
	}
	return nil
}

func shouldSkipDir(rel, name string) bool {
	for _, s := range skipDirNames {
		if name == s {
			return true
		}
	}
	for _, s := range skipRelPaths {
		if rel == s {
			return true
		}
	}
	return false
}

// parseGoFile parses a .go source file with comments attached.
func parseGoFile(fset *token.FileSet, path string) (*ast.File, error) {
	return parser.ParseFile(fset, path, nil, parser.ParseComments)
}
