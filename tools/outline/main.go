// main.go provides a source-code outliner that extracts declarations
// and signatures from Go, Python, JS/TS, Rust, and other languages.
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxLines = 100

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: outline <file>")
		os.Exit(1)
	}
	path := os.Args[1]

	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if info.IsDir() {
		fmt.Fprintln(os.Stderr, "error: path is a directory")
		os.Exit(1)
	}

	// Check for binary file (read first 512 bytes).
	if isBinary(path) {
		fmt.Println("binary file, cannot outline")
		os.Exit(0)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var lines []string

	if ext == ".go" {
		lines, err = outlineGo(path)
	} else if pattern, ok := langPatterns[ext]; ok {
		lines, err = outlineRegex(outlineRegexOptions{path: path, pattern: pattern})
	} else {
		lines, err = outlineFallback(path)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(lines) == 0 {
		fmt.Println("(no declarations found)")
		return
	}

	total := len(lines)
	if total > maxLines {
		lines = lines[:maxLines]
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if total > maxLines {
		fmt.Printf("[... %d more declarations]\n", total-maxLines)
	}
}

// isBinary checks if a file appears to be binary by looking for null bytes.
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// outlineGo uses go/parser + go/ast for precise Go file outlining.
func outlineGo(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.AllErrors)
	if err != nil && file == nil {
		// Total parse failure — fall back to regex.
		return outlineRegex(outlineRegexOptions{path: path, pattern: langPatterns[".go"]})
	}

	var lines []string

	// Package declaration.
	if file.Name != nil {
		pos := fset.Position(file.Package)
		lines = append(lines, fmt.Sprintf("%d\tpackage %s", pos.Line, file.Name.Name))
	}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			lines = append(lines, formatFunc(formatFuncOptions{fset: fset, fn: d}))
		case *ast.GenDecl:
			lines = append(lines, formatGenDecl(formatGenDeclOptions{fset: fset, decl: d})...)
		}
	}

	return lines, nil
}

type formatFuncOptions struct {
	fset *token.FileSet
	fn   *ast.FuncDecl
}

// formatFunc formats a function/method signature.
func formatFunc(opts formatFuncOptions) string {
	fset, fn := opts.fset, opts.fn
	pos := fset.Position(fn.Pos())
	var sb strings.Builder
	sb.WriteString("func ")

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		r := fn.Recv.List[0]
		sb.WriteString("(")
		if len(r.Names) > 0 {
			sb.WriteString(r.Names[0].Name)
			sb.WriteString(" ")
		}
		sb.WriteString(typeString(r.Type))
		sb.WriteString(") ")
	}

	sb.WriteString(fn.Name.Name)
	sb.WriteString(fieldListString(fn.Type.Params))

	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		results := fieldListString(fn.Type.Results)
		if len(fn.Type.Results.List) == 1 && len(fn.Type.Results.List[0].Names) == 0 {
			sb.WriteString(" ")
			sb.WriteString(typeString(fn.Type.Results.List[0].Type))
		} else {
			sb.WriteString(" ")
			sb.WriteString(results)
		}
	}

	return fmt.Sprintf("%d\t%s", pos.Line, sb.String())
}

type formatGenDeclOptions struct {
	fset *token.FileSet
	decl *ast.GenDecl
}

// formatGenDecl formats type, const, var declarations.
func formatGenDecl(opts formatGenDeclOptions) []string {
	fset, decl := opts.fset, opts.decl
	var lines []string

	switch decl.Tok {
	case token.TYPE:
		for _, spec := range decl.Specs {
			ts := spec.(*ast.TypeSpec)
			pos := fset.Position(ts.Pos())
			kind := typeKind(ts.Type)
			line := fmt.Sprintf("%d\ttype %s %s", pos.Line, ts.Name.Name, kind)
			lines = append(lines, line)

			// Show interface methods indented.
			if iface, ok := ts.Type.(*ast.InterfaceType); ok && iface.Methods != nil {
				for _, m := range iface.Methods.List {
					mpos := fset.Position(m.Pos())
					if len(m.Names) > 0 {
						if fn, ok := m.Type.(*ast.FuncType); ok {
							sig := m.Names[0].Name + fieldListString(fn.Params)
							if fn.Results != nil && len(fn.Results.List) > 0 {
								if len(fn.Results.List) == 1 && len(fn.Results.List[0].Names) == 0 {
									sig += " " + typeString(fn.Results.List[0].Type)
								} else {
									sig += " " + fieldListString(fn.Results)
								}
							}
							lines = append(lines, fmt.Sprintf("%d\t  %s", mpos.Line, sig))
						}
					} else if m.Type != nil {
						// Embedded interface.
						lines = append(lines, fmt.Sprintf("%d\t  %s", mpos.Line, typeString(m.Type)))
					}
				}
			}

			// Show struct fields (brief — just field names and types).
			if st, ok := ts.Type.(*ast.StructType); ok && st.Fields != nil {
				for _, f := range st.Fields.List {
					fpos := fset.Position(f.Pos())
					if len(f.Names) > 0 {
						lines = append(lines, fmt.Sprintf("%d\t  %s %s", fpos.Line, f.Names[0].Name, typeString(f.Type)))
					} else {
						// Embedded field.
						lines = append(lines, fmt.Sprintf("%d\t  %s", fpos.Line, typeString(f.Type)))
					}
				}
			}
		}

	case token.CONST, token.VAR:
		pos := fset.Position(decl.Pos())
		keyword := "const"
		if decl.Tok == token.VAR {
			keyword = "var"
		}
		if len(decl.Specs) == 1 {
			vs := decl.Specs[0].(*ast.ValueSpec)
			lines = append(lines, fmt.Sprintf("%d\t%s %s", pos.Line, keyword, vs.Names[0].Name))
		} else {
			// Block declaration — list names.
			names := make([]string, 0, len(decl.Specs))
			for _, s := range decl.Specs {
				vs := s.(*ast.ValueSpec)
				for _, n := range vs.Names {
					names = append(names, n.Name)
				}
			}
			if len(names) <= 5 {
				lines = append(lines, fmt.Sprintf("%d\t%s (%s)", pos.Line, keyword, strings.Join(names, ", ")))
			} else {
				lines = append(lines, fmt.Sprintf("%d\t%s (%s, ... +%d more)", pos.Line, keyword,
					strings.Join(names[:3], ", "), len(names)-3))
			}
		}
	}

	return lines
}

// typeString returns a compact string representation of an AST type expression.
func typeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeString(t.Elt)
		}
		return "[...]" + typeString(t.Elt)
	case *ast.MapType:
		return "map[" + typeString(t.Key) + "]" + typeString(t.Value)
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
		return "interface{...}"
	case *ast.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return "struct{}"
		}
		return "struct{...}"
	case *ast.FuncType:
		return "func" + fieldListString(t.Params)
	case *ast.ChanType:
		switch t.Dir {
		case ast.SEND:
			return "chan<- " + typeString(t.Value)
		case ast.RECV:
			return "<-chan " + typeString(t.Value)
		default:
			return "chan " + typeString(t.Value)
		}
	case *ast.Ellipsis:
		return "..." + typeString(t.Elt)
	case *ast.IndexExpr:
		return typeString(t.X) + "[" + typeString(t.Index) + "]"
	case *ast.IndexListExpr:
		var params []string
		for _, idx := range t.Indices {
			params = append(params, typeString(idx))
		}
		return typeString(t.X) + "[" + strings.Join(params, ", ") + "]"
	default:
		return "?"
	}
}

// fieldListString formats a parameter/result list: (name type, name type).
func fieldListString(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return "()"
	}
	var parts []string
	for _, f := range fl.List {
		t := typeString(f.Type)
		if len(f.Names) > 0 {
			for _, n := range f.Names {
				parts = append(parts, n.Name+" "+t)
			}
		} else {
			parts = append(parts, t)
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// typeKind returns the keyword for a type declaration (struct, interface, etc).
func typeKind(expr ast.Expr) string {
	switch expr.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		return typeString(expr)
	}
}

// Language-specific regex patterns for non-Go files.
var langPatterns = map[string]*regexp.Regexp{
	".py":   regexp.MustCompile(`^(class |def |async def |\s+def |\s+async def )`),
	".js":   regexp.MustCompile(`^(export |function |class |const |let |var |async function )`),
	".jsx":  regexp.MustCompile(`^(export |function |class |const |let |var |async function )`),
	".ts":   regexp.MustCompile(`^(export |function |class |const |let |var |interface |type |enum |async function )`),
	".tsx":  regexp.MustCompile(`^(export |function |class |const |let |var |interface |type |enum |async function )`),
	".rs":   regexp.MustCompile(`^(pub |fn |struct |enum |trait |impl |mod |type |use )`),
	".rb":   regexp.MustCompile(`^(class |module |def |  def )`),
	".java": regexp.MustCompile(`^(public |private |protected |class |interface |enum |abstract |static |  public |  private |  protected )`),
	".kt":   regexp.MustCompile(`^(fun |class |interface |object |data class |sealed |val |var |  fun |  val |  var )`),
	".c":    regexp.MustCompile(`^([a-zA-Z_].*\(|struct |typedef |enum |#define )`),
	".h":    regexp.MustCompile(`^([a-zA-Z_].*\(|struct |typedef |enum |#define |class )`),
	".cpp":  regexp.MustCompile(`^([a-zA-Z_].*\(|struct |typedef |enum |#define |class |namespace )`),
	".hpp":  regexp.MustCompile(`^([a-zA-Z_].*\(|struct |typedef |enum |#define |class |namespace )`),
}

type outlineRegexOptions struct {
	path    string
	pattern *regexp.Regexp
}

// outlineRegex uses regex patterns for non-Go languages.
func outlineRegex(opts outlineRegexOptions) ([]string, error) {
	f, err := os.Open(opts.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if opts.pattern.MatchString(line) {
			// Trim trailing whitespace but preserve leading (shows nesting).
			lines = append(lines, fmt.Sprintf("%d\t%s", lineNum, strings.TrimRight(line, " \t")))
		}
	}
	return lines, scanner.Err()
}

// outlineFallback shows first 20 + last 20 lines for unknown file types.
func outlineFallback(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var allLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	total := len(allLines)
	if total == 0 {
		return nil, nil
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("[%d lines total — showing first 20 + last 20]", total))

	head := 20
	if head > total {
		head = total
	}
	for i := 0; i < head; i++ {
		lines = append(lines, fmt.Sprintf("%d\t%s", i+1, allLines[i]))
	}

	if total > 40 {
		lines = append(lines, "---")
		for i := total - 20; i < total; i++ {
			lines = append(lines, fmt.Sprintf("%d\t%s", i+1, allLines[i]))
		}
	}

	return lines, nil
}
