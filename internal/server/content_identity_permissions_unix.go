//go:build !windows

package server

import "os"

func restrictContentIdentityFile(file *os.File) error {
	return file.Chmod(0o600)
}
