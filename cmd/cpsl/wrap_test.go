package main

import (
	"testing"
)

func TestWrapLineCount(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		width int
		want  int
	}{
		{"empty", "", 40, 1},
		{"short", "hello", 40, 1},
		{"exact fit", "hello world", 11, 1},
		{"one char room", "hello world", 12, 1},
		{"one wrap", "hello world foo", 11, 2},
		{"long word", "abcdefghijklmnop", 10, 2},
		{"typical chat", "lkjdf sdlkfjs dflksdjfls kdflk djsflkjs dflks", 50, 1},
		{"typical chat wrap", "lkjdf sdlkfjs dflksdjfls kdflk djsflkjs dflks", 40, 2},
		{"screenshot line1", "lkjdf sdlkfjs dflksdjfls kdflk djsflkjs dflks", 60, 1},
		{"screenshot line1 narrow", "lkjdf sdlkfjs dflksdjfls kdflk djsflkjs dflks", 45, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapLineCount(tt.line, tt.width)
			if got != tt.want {
				t.Errorf("wrapLineCount(%q, %d) = %d, want %d", tt.line, tt.width, got, tt.want)
			}
		})
	}
}
