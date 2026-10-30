package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"skillmanage/internal/scanner"
)

const contentIdentityFilename = "content-install-identities.json"

var (
	errContentIdentityConflict = errors.New("content identity conflict")
	contentRepoSegmentRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type contentInstallIdentity struct {
	RepositoryID int64  `json:"repositoryId"`
	Path         string `json:"path"`
	CanonicalDir string `json:"canonicalDir"`
	SourceRepo   string `json:"sourceRepo"`
	Name         string `json:"name"`
}

func validContentSkillPath(v string) bool {
	clean := path.Clean(v)
	return v != "" && clean == v && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(v, "/") && !strings.Contains(v, `\`)
}

func validContentGitHubRepoSlug(v string) bool {
	parts := strings.Split(v, "/")
	return len(parts) == 2 && contentRepoSegmentRe.MatchString(parts[0]) && contentRepoSegmentRe.MatchString(parts[1]) && parts[0] != "." && parts[0] != ".." && parts[1] != "." && parts[1] != ".."
}

func validateContentIdentity(record contentInstallIdentity) error {
	if record.RepositoryID <= 0 ||
		!validContentSkillPath(record.Path) ||
		!skillNameRe.MatchString(record.Name) ||
		!validContentGitHubRepoSlug(record.SourceRepo) ||
		!filepath.IsAbs(record.CanonicalDir) ||
		filepath.Clean(record.CanonicalDir) != record.CanonicalDir {
		return errors.New("invalid content identity")
	}
	return nil
}

func readContentIdentities(dir string) ([]contentInstallIdentity, error) {
	filename := filepath.Join(dir, contentIdentityFilename)
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return []contentInstallIdentity{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("invalid content identity file")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var records []contentInstallIdentity
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = []contentInstallIdentity{}
	}
	seenNames := make(map[string]bool, len(records))
	for _, record := range records {
		if err := validateContentIdentity(record); err != nil || seenNames[record.Name] {
			return nil, errors.New("invalid content identity")
		}
		seenNames[record.Name] = true
	}
	return records, nil
}

func contentIdentityState(skill scanner.Skill, canonicalDir, lockSource string, records []contentInstallIdentity, expected *contentInstallIdentity) string {
	if expected != nil && skill.FrontmatterName != expected.Name {
		return "not_match"
	}
	found := false
	for _, record := range records {
		if record.Name != skill.FrontmatterName {
			continue
		}
		found = true
		physical := record.CanonicalDir == canonicalDir && strings.EqualFold(record.SourceRepo, lockSource) && record.Name == skill.FrontmatterName
		identity := expected == nil || (record.RepositoryID == expected.RepositoryID && record.Path == expected.Path && strings.EqualFold(record.SourceRepo, expected.SourceRepo))
		if physical && identity {
			return "exact"
		}
	}
	if found {
		return "conflict"
	}
	if expected != nil && !strings.EqualFold(lockSource, expected.SourceRepo) {
		return "conflict"
	}
	if lockSource != "" && skill.FrontmatterName != "" {
		return "source_same_pending"
	}
	return "unverified"
}

func writeContentIdentities(dir string, records []contentInstallIdentity) error {
	seenNames := make(map[string]bool, len(records))
	for _, record := range records {
		if err := validateContentIdentity(record); err != nil || seenNames[record.Name] {
			return errors.New("invalid content identity")
		}
		seenNames[record.Name] = true
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".content-identities-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := restrictContentIdentityFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(dir, contentIdentityFilename)); err != nil {
		return fmt.Errorf("replace content identity file: %w", err)
	}
	// Windows does not support fsync on directory handles. The file itself is
	// synced before rename there; Unix platforms also sync the parent directory
	// so the name change is durable.
	if runtime.GOOS == "windows" {
		return nil
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func sameContentIdentity(a, b contentInstallIdentity) bool {
	return a.RepositoryID == b.RepositoryID && a.Path == b.Path && a.CanonicalDir == b.CanonicalDir && a.SourceRepo == b.SourceRepo && a.Name == b.Name
}

// upsertContentIdentity must be called while holding the server's existing
// mutex so concurrent install routes cannot lose one another's records.
func upsertContentIdentity(dir string, candidate contentInstallIdentity) error {
	if err := validateContentIdentity(candidate); err != nil {
		return err
	}
	records, err := readContentIdentities(dir)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Name != candidate.Name {
			continue
		}
		if sameContentIdentity(record, candidate) {
			return nil
		}
		return errContentIdentityConflict
	}
	return writeContentIdentities(dir, append(records, candidate))
}
