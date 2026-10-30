//go:build windows

package server

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func assertContentIdentityFilePrivate(t *testing.T, path string) {
	t.Helper()
	security, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := security.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl.AceCount != 1 {
		t.Fatalf("content identity DACL has %d ACEs, want only the current user's ACE", dacl.AceCount)
	}
	control, _, err := security.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("content identity DACL is inheritable; want a protected DACL")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	wantACE := "D:P(A;;GA;;;" + user.User.Sid.String() + ")"
	if !strings.Contains(security.String(), wantACE) {
		t.Fatalf("content identity DACL = %q, want only current-user full-control ACE %q", security.String(), wantACE)
	}
}
