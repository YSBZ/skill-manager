package toolchain

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	systemNodeRequiredVersion = "22.20.0"
	nodeDownloadURL           = "https://nodejs.org/en/download"
	nodeProbeTimeout          = 2 * time.Second
	maxNodeVersionOutput      = 4 * 1024
)

type NodeDependency struct {
	State           string `json:"state"`
	Version         string `json:"version,omitempty"`
	RequiredVersion string `json:"requiredVersion"`
	DownloadURL     string `json:"downloadURL"`
}

type parsedNodeVersion struct {
	major uint64
	minor uint64
	patch uint64
	raw   string
}

func parseNodeVersion(output string) (parsedNodeVersion, error) {
	raw := strings.TrimSpace(output)
	version := raw
	if strings.HasPrefix(version, "v") {
		version = strings.TrimPrefix(version, "v")
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return parsedNodeVersion{}, errors.New("version must have three numeric components")
	}
	values := [3]uint64{}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return parsedNodeVersion{}, errors.New("version component is not canonical")
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return parsedNodeVersion{}, errors.New("version contains non-numeric components")
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsedNodeVersion{}, errors.New("version component is out of range")
		}
		values[i] = value
	}
	return parsedNodeVersion{major: values[0], minor: values[1], patch: values[2], raw: raw}, nil
}

func (version parsedNodeVersion) atLeast(major, minor, patch uint64) bool {
	if version.major != major {
		return version.major > major
	}
	if version.minor != minor {
		return version.minor > minor
	}
	return version.patch >= patch
}

func nodeSupportsSystemCA(version parsedNodeVersion) bool {
	if version.major == 22 {
		return version.minor >= 19
	}
	if version.major == 24 {
		return version.minor >= 6
	}
	return version.major >= 25
}

func nodeSupportsEnvironmentProxy(version parsedNodeVersion) bool {
	if version.major == 22 {
		return version.minor >= 21
	}
	return version.major >= 24
}

type boundedVersionOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (output *boundedVersionOutput) Write(data []byte) (int, error) {
	remaining := output.limit - output.buffer.Len()
	if remaining >= len(data) {
		return output.buffer.Write(data)
	}
	if remaining > 0 {
		_, _ = output.buffer.Write(data[:remaining])
	}
	output.overflow = true
	return len(data), nil
}

func inspectSystemNode(goos string) (string, *NodeDependency, parsedNodeVersion) {
	dependency := &NodeDependency{
		State:           "missing",
		RequiredVersion: systemNodeRequiredVersion,
		DownloadURL:     nodeDownloadURL,
	}
	commandName := "node"
	if goos == "windows" {
		commandName = "node.exe"
	}
	selected, err := exec.LookPath(commandName)
	if err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			dependency.State = "invalid"
		}
		return "", dependency, parsedNodeVersion{}
	}
	selected, err = filepath.Abs(selected)
	if err != nil {
		dependency.State = "invalid"
		return "", dependency, parsedNodeVersion{}
	}
	info, err := os.Stat(selected)
	if err != nil || !info.Mode().IsRegular() {
		dependency.State = "invalid"
		return "", dependency, parsedNodeVersion{}
	}
	if goos == "windows" {
		if !strings.EqualFold(filepath.Ext(selected), ".exe") {
			dependency.State = "invalid"
			return "", dependency, parsedNodeVersion{}
		}
	} else if info.Mode().Perm()&0o111 == 0 {
		dependency.State = "invalid"
		return "", dependency, parsedNodeVersion{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), nodeProbeTimeout)
	defer cancel()
	stdout := &boundedVersionOutput{limit: maxNodeVersionOutput}
	cmd := newCommand(ctx, selected, "--version")
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || stdout.overflow {
		dependency.State = "invalid"
		return "", dependency, parsedNodeVersion{}
	}
	version, err := parseNodeVersion(stdout.buffer.String())
	if err != nil {
		dependency.State = "invalid"
		return "", dependency, parsedNodeVersion{}
	}
	dependency.Version = version.raw
	if !version.atLeast(22, 20, 0) {
		dependency.State = "too_old"
		return "", dependency, version
	}
	dependency.State = "ready"
	return selected, dependency, version
}

// InstallerCommand returns the command key and current Node dependency state
// for skill-install operations. A false managed result means development mode
// should retain its existing system-npx lookup behavior.
func InstallerCommand() (path string, dependency *NodeDependency, managed bool) {
	rt := activeRuntime()
	if rt == nil {
		return "", nil, false
	}
	switch rt.schemaVersion {
	case manifestSchemaV1:
		return rt.npxPath, nil, true
	case manifestSchemaV2:
		if rt.nodeDependency == nil {
			return "", &NodeDependency{
				State:           "invalid",
				RequiredVersion: systemNodeRequiredVersion,
				DownloadURL:     nodeDownloadURL,
			}, true
		}
		snapshot := *rt.nodeDependency
		if snapshot.State == "ready" {
			return rt.nodePath, &snapshot, true
		}
		return "", &snapshot, true
	default:
		return "", nil, false
	}
}

func nodeVersionForActivation(rt *runtimeBundle) (parsedNodeVersion, bool) {
	if rt == nil {
		return parsedNodeVersion{}, false
	}
	if rt.schemaVersion == manifestSchemaV1 {
		return rt.nodeVersion, true
	}
	if rt.schemaVersion == manifestSchemaV2 && rt.nodeDependency != nil && rt.nodeDependency.State == "ready" {
		return rt.nodeVersion, true
	}
	return parsedNodeVersion{}, false
}
