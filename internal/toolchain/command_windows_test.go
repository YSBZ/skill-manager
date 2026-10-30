//go:build windows

package toolchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const windowsCancelRoleEnv = "SKILLMANAGE_WINDOWS_CANCEL_ROLE"
const windowsCancelPIDsEnv = "SKILLMANAGE_WINDOWS_CANCEL_PIDS"
const windowsHangHelperEnv = "SKILLMANAGE_WINDOWS_HANG_HELPER"

func TestVersionProbeCommandDoesNotCreateWindowsConsole(t *testing.T) {
	cmd := newCommand(context.Background(), `C:\Program Files\nodejs\node.exe`, "--version")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("Node version probes must inherit the windowless command policy")
	}
}

func TestNpxCommandCancellationStopsWindowsChildProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child-pids.txt")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := newCommand(ctx, executable, "-test.run=^TestWindowsCancellationChildHelper$")
	cmd.Env = append(os.Environ(), windowsCancelRoleEnv+"=child", windowsCancelPIDsEnv+"="+pidFile)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
	})

	deadline := time.Now().Add(8 * time.Second)
	var childPID, grandchildPID uint32
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				child, childErr := strconv.ParseUint(fields[0], 10, 32)
				grandchild, grandchildErr := strconv.ParseUint(fields[1], 10, 32)
				if childErr == nil && grandchildErr == nil {
					childPID, grandchildPID = uint32(child), uint32(grandchild)
				}
				if childPID != 0 && grandchildPID != 0 {
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 || grandchildPID == 0 {
		t.Fatalf("child processes did not report their PIDs before timeout: child=%d grandchild=%d", childPID, grandchildPID)
	}

	cancel()
	waitErr := cmd.Wait()
	waited = true
	if waitErr == nil {
		t.Fatal("cancelled Windows process tree succeeded")
	}
	for _, pid := range []uint32{uint32(childPID), uint32(grandchildPID)} {
		if err := waitForWindowsProcessExit(pid, 5*time.Second); err != nil {
			t.Errorf("process %d survived cancellation: %v", pid, err)
		}
	}
}

func TestWindowsHiddenCommandHonorsContextDeadline(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(windowsHangHelperEnv, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = runHiddenWindowsCommand(ctx, executable, "-test.run=^TestWindowsCancellationHangHelper$")
	if err == nil {
		t.Fatal("context deadline did not stop the child command")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancelled child command took %s, expected bounded exit", elapsed)
	}
}

func TestWindowsCancellationChildHelper(t *testing.T) {
	if os.Getenv(windowsCancelRoleEnv) != "child" {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	grandchild := exec.Command(executable, "-test.run=^TestWindowsCancellationGrandchild$")
	grandchild.Env = append(os.Environ(), windowsCancelRoleEnv+"=grandchild")
	if err := grandchild.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := os.Getenv(windowsCancelPIDsEnv)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d %d\n", os.Getpid(), grandchild.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestWindowsCancellationGrandchild(t *testing.T) {
	if os.Getenv(windowsCancelRoleEnv) != "grandchild" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestWindowsCancellationHangHelper(t *testing.T) {
	if os.Getenv(windowsHangHelperEnv) != "1" {
		return
	}
	select {}
}

func waitForWindowsProcessExit(pid uint32, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		if err != nil {
			return err
		}
		state, waitErr := windows.WaitForSingleObject(handle, 0)
		_ = windows.CloseHandle(handle)
		if waitErr != nil {
			return waitErr
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit before timeout", pid)
}
