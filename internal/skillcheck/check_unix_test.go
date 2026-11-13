//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package skillcheck

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestReadContentDoesNotOpenFIFOs(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "pipe-link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{fifo, link} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := ReadContent(path)
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("ReadContent accepted a FIFO")
				}
			case <-time.After(250 * time.Millisecond):
				// Release an implementation that incorrectly opened the pipe before
				// failing the test, so the goroutine does not linger.
				if writer, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = writer.Close()
				}
				t.Fatal("ReadContent blocked while opening a FIFO")
			}
		})
	}
}
