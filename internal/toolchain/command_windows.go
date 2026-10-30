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
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsTreeKillTimeout = 2 * time.Second
	windowsTreeKillWait    = 250 * time.Millisecond
)

func configureCancellation(cmd *exec.Cmd) {
	// Version probes run before the server runner applies its own window flags.
	// Keep those startup checks windowless as well as normal installer commands.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		treeErr := killWindowsProcessTree(cmd.Process.Pid)
		if treeErr == nil {
			return nil
		}
		processErr := cmd.Process.Kill()
		if errors.Is(processErr, os.ErrProcessDone) {
			return treeErr
		}
		if processErr != nil {
			return errors.Join(treeErr, processErr)
		}
		// The target process is gone, but taskkill's failure means descendants
		// may remain. Report that incomplete tree cleanup to the caller.
		return treeErr
	}
}

func killWindowsProcessTree(pid int) error {
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return fmt.Errorf("resolve System32 for process-tree cleanup: %w", err)
	}
	taskkillPath := filepath.Join(systemDir, "taskkill.exe")
	ctx, cancel := context.WithTimeout(context.Background(), windowsTreeKillTimeout)
	defer cancel()
	return runWindowsTreeKill(ctx, taskkillPath, pid)
}

func runWindowsTreeKill(ctx context.Context, taskkillPath string, pid int) error {
	if !filepath.IsAbs(taskkillPath) {
		return fmt.Errorf("System32 taskkill path is not absolute: %q", taskkillPath)
	}
	return runHiddenWindowsCommand(ctx, taskkillPath, "/PID", strconv.Itoa(pid), "/T", "/F")
}

func runHiddenWindowsCommand(ctx context.Context, path string, args ...string) error {
	killer := exec.CommandContext(ctx, path, args...)
	killer.WaitDelay = windowsTreeKillWait
	killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return killer.Run()
}
