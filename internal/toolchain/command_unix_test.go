//go:build !windows

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNpxCommandCancellationStopsChildProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	scriptPath := filepath.Join(t.TempDir(), "fake-npx")
	script := fmt.Sprintf("#!/bin/sh\n/bin/sleep 30 &\necho $! > %q\nwait\n", pidFile)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := NpxCommand(ctx, scriptPath, "unused")
	if cmd.WaitDelay <= 0 {
		t.Fatalf("NpxCommand WaitDelay = %s", cmd.WaitDelay)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		_ = cmd.Wait()
		t.Fatal("fake npx did not start its child")
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled npx command succeeded")
	}
	for deadline = time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child process %d survived cancellation", childPID)
}
