package main

import (
	"os"
	"testing"
)

// TestMain runs all tests in a temp directory so that saveConfig() calls
// never clobber the real ~/.herm/config.json.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "herm-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	orig, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	defer os.Chdir(orig)

	os.Exit(m.Run())
}

// Tests that depend on the old TextInput/Renderer/configForm/modelList
// types have been removed. They will be rewritten in Phase 5b.

// --- visibleLen tests ---

func TestVisibleLen_PlainText(t *testing.T) {
	if got := visibleLen("hello"); got != 5 {
		t.Errorf("visibleLen = %d, want 5", got)
	}
}

func TestVisibleLen_Empty(t *testing.T) {
	if got := visibleLen(""); got != 0 {
		t.Errorf("visibleLen = %d, want 0", got)
	}
}

func TestVisibleLen_AnsiOnly(t *testing.T) {
	if got := visibleLen("\033[33m\033[0m"); got != 0 {
		t.Errorf("visibleLen = %d, want 0 (ANSI only)", got)
	}
}

func TestVisibleLen_AnsiWrapped(t *testing.T) {
	// "\033[33m(offline)\033[0m" — visible part is "(offline)" = 9 chars
	if got := visibleLen("\033[33m(offline)\033[0m"); got != 9 {
		t.Errorf("visibleLen = %d, want 9", got)
	}
}

func TestVisibleLen_MixedAnsiAndText(t *testing.T) {
	// "model \033[33m(offline)\033[0m" — visible = "model (offline)" = 15
	s := "model \033[33m(offline)\033[0m"
	if got := visibleLen(s); got != 15 {
		t.Errorf("visibleLen = %d, want 15", got)
	}
}

func TestVisibleLen_MultipleEscapes(t *testing.T) {
	// bold + color + reset
	s := "\033[1m\033[34mtext\033[0m"
	if got := visibleLen(s); got != 4 {
		t.Errorf("visibleLen = %d, want 4", got)
	}
}
