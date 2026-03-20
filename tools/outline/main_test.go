package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOutlineGo_FunctionsAndTypes(t *testing.T) {
	src := `package example

import "fmt"

type Config struct {
	Name string
	Value int
}

type Handler interface {
	Handle(req Request) (Response, error)
	Close() error
}

func NewConfig(name string) *Config {
	return &Config{Name: name}
}

func (c *Config) String() string {
	return fmt.Sprintf("%s=%d", c.Name, c.Value)
}

var defaultConfig = NewConfig("default")

const maxRetries = 3
`
	path := writeTemp(t, "test.go", src)
	lines, err := outlineGo(path)
	if err != nil {
		t.Fatalf("outlineGo: %v", err)
	}

	// Should contain: package, type Config struct, type Handler interface,
	// func NewConfig, func (c *Config) String, var defaultConfig, const maxRetries
	text := strings.Join(lines, "\n")

	expects := []string{
		"package example",
		"type Config struct",
		"type Handler interface",
		"Handle(req Request) (Response, error)",
		"Close() error",
		"func NewConfig(name string) *Config",
		"func (c *Config) String() string",
		"var defaultConfig",
		"const maxRetries",
	}
	for _, e := range expects {
		if !strings.Contains(text, e) {
			t.Errorf("expected %q in output, got:\n%s", e, text)
		}
	}

	// Each line should have a line number prefix.
	for _, l := range lines {
		if len(l) == 0 {
			continue
		}
		if l[0] < '0' || l[0] > '9' {
			// Indented lines (struct fields, interface methods) also have numbers.
			if !strings.Contains(l, "\t") {
				t.Errorf("expected line number prefix, got: %q", l)
			}
		}
	}
}

func TestOutlineGo_Generics(t *testing.T) {
	src := `package example

type Set[T comparable] struct {
	items map[T]struct{}
}

func NewSet[T comparable]() *Set[T] {
	return &Set[T]{items: make(map[T]struct{})}
}

func (s *Set[T]) Add(item T) {
	s.items[item] = struct{}{}
}
`
	path := writeTemp(t, "generics.go", src)
	lines, err := outlineGo(path)
	if err != nil {
		t.Fatalf("outlineGo: %v", err)
	}

	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "Set") {
		t.Errorf("expected Set type, got:\n%s", text)
	}
	if !strings.Contains(text, "NewSet") {
		t.Errorf("expected NewSet func, got:\n%s", text)
	}
	if !strings.Contains(text, "Add") {
		t.Errorf("expected Add method, got:\n%s", text)
	}
}

func TestOutlineGo_SyntaxError(t *testing.T) {
	src := `package broken

func valid() {
}

func invalid( {  // syntax error
}
`
	path := writeTemp(t, "broken.go", src)
	// Should degrade gracefully — either partial AST or regex fallback.
	lines, err := outlineGo(path)
	if err != nil {
		t.Fatalf("outlineGo should handle syntax errors: %v", err)
	}
	// Should at least find the package declaration.
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "package") {
		t.Errorf("expected at least package declaration, got:\n%s", text)
	}
}

func TestOutlineRegex_Python(t *testing.T) {
	src := `import os

class MyClass:
    def __init__(self):
        pass

    def method(self):
        return True

def standalone(x, y):
    return x + y

async def async_func():
    pass
`
	path := writeTemp(t, "test.py", src)
	pattern := langPatterns[".py"]
	lines, err := outlineRegex(path, pattern)
	if err != nil {
		t.Fatalf("outlineRegex: %v", err)
	}

	text := strings.Join(lines, "\n")
	expects := []string{"class MyClass", "def __init__", "def method", "def standalone", "async def async_func"}
	for _, e := range expects {
		if !strings.Contains(text, e) {
			t.Errorf("expected %q in output, got:\n%s", e, text)
		}
	}
}

func TestOutlineRegex_TypeScript(t *testing.T) {
	src := `export interface User {
    name: string;
    age: number;
}

export class UserService {
    async getUser(id: string): Promise<User> {
        return fetch(id);
    }
}

export function helper(): void {}

type Config = {
    debug: boolean;
};

enum Status {
    Active,
    Inactive,
}
`
	path := writeTemp(t, "test.ts", src)
	pattern := langPatterns[".ts"]
	lines, err := outlineRegex(path, pattern)
	if err != nil {
		t.Fatalf("outlineRegex: %v", err)
	}

	text := strings.Join(lines, "\n")
	expects := []string{"export interface User", "export class UserService", "export function helper", "type Config", "enum Status"}
	for _, e := range expects {
		if !strings.Contains(text, e) {
			t.Errorf("expected %q in output, got:\n%s", e, text)
		}
	}
}

func TestOutlineFallback_UnknownExtension(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 50; i++ {
		sb.WriteString("line content\n")
	}
	path := writeTemp(t, "test.xyz", sb.String())
	lines, err := outlineFallback(path)
	if err != nil {
		t.Fatalf("outlineFallback: %v", err)
	}

	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "50 lines total") {
		t.Errorf("expected total line count, got:\n%s", text)
	}
	if !strings.Contains(text, "---") {
		t.Errorf("expected separator between head and tail, got:\n%s", text)
	}
}

func TestOutlineFallback_EmptyFile(t *testing.T) {
	path := writeTemp(t, "empty.xyz", "")
	lines, err := outlineFallback(path)
	if err != nil {
		t.Fatalf("outlineFallback: %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("expected no lines for empty file, got %d", len(lines))
	}
}

func TestIsBinary(t *testing.T) {
	// Text file.
	textPath := writeTemp(t, "text.txt", "hello world\n")
	if isBinary(textPath) {
		t.Error("expected text file to not be binary")
	}

	// Binary file (contains null bytes).
	binPath := writeTemp(t, "binary.bin", "hello\x00world")
	if !isBinary(binPath) {
		t.Error("expected binary file to be detected")
	}
}

func TestFileNotFound(t *testing.T) {
	_, err := outlineGo("/nonexistent/file.go")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

// writeTemp creates a temporary file with the given name and content.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
