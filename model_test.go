package main

import (
	"os"
	"testing"
)

// TestMain runs all tests in a temp directory so that saveConfig() calls
// never clobber the real ~/.cpsl/config.json.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "cpsl-test-*")
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
