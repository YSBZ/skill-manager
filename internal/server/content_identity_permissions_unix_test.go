//go:build !windows

package server

import (
	"os"
	"testing"
)

func assertContentIdentityFilePrivate(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("content identity file permissions = %o, want 600", got)
	}
}
