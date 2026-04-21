package main

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestFileLength(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantHits   int
		wantSubstr string
	}{
		{
			name:     "pass under limit",
			args:     []string{"--max-lines=100", "testdata/file_length/short.go"},
			wantHits: 0,
		},
		{
			name:       "fail over limit",
			args:       []string{"--max-lines=5", "testdata/file_length/long.go"},
			wantHits:   1,
			wantSubstr: "file has",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runFileLength(tt.args)
			if err != nil {
				t.Fatalf("runFileLength: %v", err)
			}
			if len(got) != tt.wantHits {
				t.Fatalf("got %d violations, want %d: %+v", len(got), tt.wantHits, got)
			}
			if tt.wantSubstr != "" && !strings.Contains(got[0].Message, tt.wantSubstr) {
				t.Errorf("message %q missing %q", got[0].Message, tt.wantSubstr)
			}
		})
	}
}

func TestDocstring(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantHits   int
		wantSubstr string
	}{
		{"missing", "testdata/docstring/missing.go", 1, "missing file doc comment"},
		{"too short", "testdata/docstring/short.go", 1, "chars (min"},
		{"too many lines", "testdata/docstring/toolong.go", 1, "lines (max"},
		{"ok", "testdata/docstring/ok.go", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runDocstring([]string{tt.path})
			if err != nil {
				t.Fatalf("runDocstring: %v", err)
			}
			if len(got) != tt.wantHits {
				t.Fatalf("got %d violations, want %d: %+v", len(got), tt.wantHits, got)
			}
			if tt.wantSubstr != "" && !strings.Contains(got[0].Message, tt.wantSubstr) {
				t.Errorf("message %q missing %q", got[0].Message, tt.wantSubstr)
			}
		})
	}
}

func TestPositionalParams(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantHits int
	}{
		{"multi violations", "testdata/positional/multi.go", 3},
		{"ctx exempt passes", "testdata/positional/ctx.go", 0},
		{"ctx does not rescue extra", "testdata/positional/ctx_fail.go", 1},
		{"variadic passes", "testdata/positional/variadic.go", 0},
		{"variadic does not rescue extra", "testdata/positional/variadic_fail.go", 1},
		{"interface methods skipped", "testdata/positional/iface.go", 0},
		{"ok baseline", "testdata/positional/ok.go", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := runPositionalParams([]string{tt.path})
			if err != nil {
				t.Fatalf("runPositionalParams: %v", err)
			}
			if len(got) != tt.wantHits {
				t.Fatalf("got %d violations, want %d: %+v", len(got), tt.wantHits, got)
			}
		})
	}
}

func TestWalkGoFilesSkipsExclusions(t *testing.T) {
	var got []string
	err := walkGoFiles([]string{"testdata/walk"}, func(p string) error {
		got = append(got, filepath.ToSlash(p))
		return nil
	})
	if err != nil {
		t.Fatalf("walkGoFiles: %v", err)
	}
	sort.Strings(got)
	want := []string{"testdata/walk/included.go"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("walker yielded %v, want %v", got, want)
	}
}
