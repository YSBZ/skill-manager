package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"skillmanage/internal/config"
	"skillmanage/internal/content"
	"skillmanage/internal/scanner"
	"skillmanage/internal/source"
)

var (
	errContentIdentityMismatch      = errors.New("content skill identity mismatch")
	errInvalidContentInstallRequest = errors.New("invalid content skill identity")
)

type contentInstallRequest struct {
	EntryID      string `json:"entryId"`
	RepositoryID int64  `json:"repositoryId"`
	Path         string `json:"path"`
	Name         string `json:"name"`
}

func (r *contentInstallRequest) UnmarshalJSON(data []byte) error {
	type plain contentInstallRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded plain
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid content identity JSON")
	}
	*r = contentInstallRequest(decoded)
	return nil
}

type contentSkillRef struct {
	RepositoryID int64  `json:"repositoryId"`
	Repo         string `json:"repo"`
	Path         string `json:"path"`
	Name         string `json:"name"`
	Verified     bool   `json:"verified"`
}

type contentEntryEnvelope struct {
	Entry struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Project *struct {
			RepositoryID int64             `json:"repositoryId"`
			Repo         string            `json:"repo"`
			Type         string            `json:"type"`
			Skills       []contentSkillRef `json:"skills"`
		} `json:"project"`
	} `json:"entry"`
}

type resolvedContentSkill struct {
	Ref     contentSkillRef
	RepoURL string
}

func (s *Server) resolveContentSkillRef(ctx context.Context, req contentInstallRequest) (resolvedContentSkill, error) {
	if s.contentBaseURL == nil || s.contentHTTPClient == nil {
		return resolvedContentSkill{}, errors.New("content service is unavailable")
	}
	if err := validateContentInstallRequest(req); err != nil {
		return resolvedContentSkill{}, err
	}
	target := *s.contentBaseURL
	target.Path = strings.TrimRight(target.Path, "/") + "/v1/entries/" + req.EntryID
	target.RawPath = ""
	target.RawQuery = ""
	target.ForceQuery = false
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return resolvedContentSkill{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.contentHTTPClient.Do(request)
	if err != nil {
		return resolvedContentSkill{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return resolvedContentSkill{}, fmt.Errorf("content entry returned %d", response.StatusCode)
	}
	var data contentEntryEnvelope
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&data); err != nil {
		return resolvedContentSkill{}, err
	}
	project := data.Entry.Project
	if data.Entry.ID != req.EntryID || data.Entry.Kind != "project" || project == nil || project.Type != "skill" || project.RepositoryID != req.RepositoryID || !validContentGitHubRepoSlug(project.Repo) {
		return resolvedContentSkill{}, errContentIdentityMismatch
	}
	count := 0
	var match contentSkillRef
	for _, ref := range project.Skills {
		if ref.RepositoryID == project.RepositoryID && strings.EqualFold(ref.Repo, project.Repo) && strings.EqualFold(ref.Name, req.Name) {
			count++
			match = ref
		}
	}
	if count != 1 || !match.Verified || match.Name != req.Name || match.Path != req.Path || !validContentSkillPath(match.Path) {
		return resolvedContentSkill{}, errContentIdentityMismatch
	}
	return resolvedContentSkill{Ref: match, RepoURL: "https://github.com/" + project.Repo}, nil
}

func validateContentInstallRequest(req contentInstallRequest) error {
	if content.ValidateID(req.EntryID) != nil || req.RepositoryID <= 0 || !validContentSkillPath(req.Path) || req.Name == "." || req.Name == ".." || !skillNameRe.MatchString(req.Name) {
		return errInvalidContentInstallRequest
	}
	return nil
}

func existingContentInstallState(root string, lock source.SkillLock, records []contentInstallIdentity, ref resolvedContentSkill) (string, error) {
	hasRecord := false
	for _, record := range records {
		if strings.EqualFold(record.Name, ref.Ref.Name) {
			hasRecord = true
			if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
				return "conflict", nil
			}
		}
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	} else if err != nil {
		return "conflict", err
	}
	skills, err := scanner.Scan(root)
	if err != nil {
		return "conflict", err
	}
	matches := make([]scanner.Skill, 0, 1)
	for _, skill := range skills {
		if strings.EqualFold(skill.LinkName, ref.Ref.Name) || strings.EqualFold(skill.LogicalName, ref.Ref.Name) || strings.EqualFold(skill.FrontmatterName, ref.Ref.Name) {
			matches = append(matches, skill)
		}
	}
	if len(matches) == 0 {
		if hasRecord {
			return "conflict", nil
		}
		return "absent", nil
	}
	if len(matches) != 1 {
		return "conflict", nil
	}
	skill := matches[0]
	entry, ok := lock.Has(skill.LinkName)
	if !ok {
		entry, ok = lock.Has(filepath.Base(skill.Dir))
	}
	if !ok {
		return "conflict", nil
	}
	canonical, err := filepath.EvalSymlinks(skill.Dir)
	if err != nil {
		return "conflict", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "conflict", err
	}
	expected := contentInstallIdentity{RepositoryID: ref.Ref.RepositoryID, Path: ref.Ref.Path, SourceRepo: ref.Ref.Repo, Name: ref.Ref.Name}
	status := contentIdentityState(skill, filepath.Clean(canonical), entry.Source, records, &expected)
	if status == "unverified" || status == "not_match" {
		return "conflict", nil
	}
	return status, nil
}

func verifyInstalledContentSkill(root string, lock source.SkillLock, ref resolvedContentSkill) (contentInstallIdentity, error) {
	skills, err := scanner.Scan(root)
	if err != nil {
		return contentInstallIdentity{}, err
	}
	matches := make([]scanner.Skill, 0, 1)
	for _, skill := range skills {
		if skill.FrontmatterName == ref.Ref.Name {
			matches = append(matches, skill)
		}
	}
	if len(matches) != 1 {
		return contentInstallIdentity{}, errors.New("installed skill missing or ambiguous")
	}
	skill := matches[0]
	entry, ok := lock.Has(skill.LinkName)
	if !ok {
		entry, ok = lock.Has(filepath.Base(skill.Dir))
	}
	if !ok || !strings.EqualFold(entry.Source, ref.Ref.Repo) || skill.FrontmatterName != ref.Ref.Name {
		return contentInstallIdentity{}, errors.New("installed source/name mismatch")
	}
	canonical, err := filepath.EvalSymlinks(skill.Dir)
	if err != nil {
		return contentInstallIdentity{}, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return contentInstallIdentity{}, err
	}
	canonical = filepath.Clean(canonical)
	root, err = filepath.Abs(root)
	if err != nil {
		return contentInstallIdentity{}, err
	}
	rel, err := filepath.Rel(filepath.Clean(root), canonical)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return contentInstallIdentity{}, errors.New("installed skill is outside canonical destination")
	}
	return contentInstallIdentity{
		RepositoryID: ref.Ref.RepositoryID,
		Path:         ref.Ref.Path,
		CanonicalDir: canonical,
		SourceRepo:   entry.Source,
		Name:         skill.FrontmatterName,
	}, nil
}

// contentInstallDestination follows the fixed CLI global root, independent of
// user-registered directory sources. The lock remains beside the lexical root.
func (s *Server) contentInstallDestination() (canonicalRoot, lockDir string, err error) {
	home := os.UserHomeDir
	if s.contentInstallHomeDir != nil {
		home = s.contentInstallHomeDir
	}
	homeDir, err := home()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		if err == nil {
			err = errors.New("home directory is unavailable")
		}
		return "", "", err
	}
	homeDir, err = filepath.Abs(homeDir)
	if err != nil {
		return "", "", err
	}
	homeDir, err = filepath.EvalSymlinks(homeDir)
	if err != nil {
		return "", "", err
	}
	lockDir = filepath.Join(homeDir, ".agents")
	lexicalRoot := filepath.Join(lockDir, "skills")
	parentInfo, parentErr := os.Lstat(lockDir)
	resolvedParent := lockDir
	if parentErr == nil {
		resolvedParent, err = filepath.EvalSymlinks(lockDir)
		if err != nil {
			return "", "", err
		}
		parentInfo, err = os.Stat(resolvedParent)
		if err != nil {
			return "", "", err
		}
		if !parentInfo.IsDir() {
			return "", "", errors.New("canonical skills parent is not a directory")
		}
	} else if !errors.Is(parentErr, os.ErrNotExist) {
		return "", "", parentErr
	}
	if _, statErr := os.Lstat(lexicalRoot); errors.Is(statErr, os.ErrNotExist) {
		return filepath.Clean(filepath.Join(resolvedParent, "skills")), lockDir, nil
	} else if statErr != nil {
		return "", "", statErr
	}
	resolved, err := filepath.EvalSymlinks(lexicalRoot)
	if err != nil {
		return "", "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", "", err
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", err
	}
	if !info.IsDir() {
		return "", "", errors.New("canonical skills destination is not a directory")
	}
	return resolved, lockDir, nil
}

func (s *Server) handleContentSkillInstall(w http.ResponseWriter, identity contentInstallRequest) {
	s.mu.Lock()
	npx, runner, nodeDependency := s.npxPath, s.runner, s.nodeDependency
	s.mu.Unlock()
	if npx == "" || runner == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": npxUnavailableErr("npx skills add", nodeDependency)})
		return
	}
	ctx, cancel := context.WithTimeout(s.detachedCtx(), 300*time.Second)
	defer cancel()
	resolved, err := s.resolveContentSkillRef(ctx, identity)
	if err != nil {
		switch {
		case errors.Is(err, errContentIdentityMismatch):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "authoritative content identity did not match"})
		case errors.Is(err, errInvalidContentInstallRequest):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "authoritative content identity lookup failed"})
		}
		return
	}

	s.installMu.Lock()
	defer s.installMu.Unlock()
	root, lockDir, err := s.contentInstallDestination()
	if err != nil {
		writeContentInstallBlock(w, "conflict", "canonical skills destination could not be verified")
		return
	}
	if !s.contentInstallSourceMappingMatches(root) {
		writeContentInstallBlock(w, "conflict", "configured @agents source does not match the CLI destination")
		return
	}
	lock, err := source.LoadSkillLock(lockDir)
	if err != nil {
		writeContentInstallBlock(w, "conflict", "skills.sh lockfile could not be verified")
		return
	}
	records, err := readContentIdentities(s.centralDir)
	if err != nil {
		writeContentInstallBlock(w, "conflict", "content identity records could not be verified")
		return
	}
	state, err := existingContentInstallState(root, lock, records, resolved)
	if err != nil {
		writeContentInstallBlock(w, "conflict", "canonical skills directory could not be scanned")
		return
	}
	if state == "exact" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "already-installed", "identityStatus": "exact"})
		return
	}
	if state != "absent" {
		writeContentInstallBlock(w, state, "an existing skill has the same name or an unverified source")
		return
	}

	stdout, stderr, runErr := runner.SkillsAddURL(ctx, npx, resolved.RepoURL, resolved.Ref.Name)
	clean := ansiRe.ReplaceAllString(stdout+"\n"+stderr, "")
	if runErr != nil || !(strings.Contains(clean, "✓") || strings.Contains(clean, "Installed") || strings.Contains(clean, "copied")) {
		resp := map[string]any{"ok": false, "status": "failed", "stdout": stdout, "stderr": stderr}
		if runErr != nil {
			resp["error"] = runErr.Error()
		} else {
			resp["error"] = skillsAddFailReason(clean)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	postflightRoot, postflightLockDir, err := s.contentInstallDestination()
	if err != nil || filepath.Clean(postflightRoot) != filepath.Clean(root) || !s.contentInstallSourceMappingMatches(postflightRoot) {
		writeContentInstallPending(w, "conflict", stdout, stderr, "canonical skills destination changed during install")
		return
	}
	postflightLock, err := source.LoadSkillLock(postflightLockDir)
	if err != nil {
		writeContentInstallPending(w, "conflict", stdout, stderr, "skills.sh lockfile could not be verified after install")
		return
	}
	candidate, err := verifyInstalledContentSkill(postflightRoot, postflightLock, resolved)
	if err != nil {
		writeContentInstallPending(w, "not_match", stdout, stderr, err.Error())
		return
	}
	s.mu.Lock()
	err = upsertContentIdentity(s.centralDir, candidate)
	s.mu.Unlock()
	if err != nil {
		status := "unverified"
		if errors.Is(err, errContentIdentityConflict) {
			status = "conflict"
		}
		writeContentInstallPending(w, status, stdout, stderr, "content identity proof could not be saved")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "installed", "identityStatus": "exact", "stdout": stdout, "stderr": stderr})
}

func writeContentInstallBlock(w http.ResponseWriter, identityStatus, message string) {
	writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "status": "identity_conflict", "identityStatus": identityStatus, "error": message})
}

func writeContentInstallPending(w http.ResponseWriter, identityStatus, stdout, stderr, message string) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "installed_pending", "identityStatus": identityStatus, "stdout": stdout, "stderr": stderr, "error": message})
}

func (s *Server) contentInstallSourceMappingMatches(canonicalRoot string) bool {
	s.mu.Lock()
	configured := append([]config.DirectorySource(nil), s.cfg.DirectorySources...)
	s.mu.Unlock()
	var paths []string
	if s.contentInstallHomeDir == nil {
		paths = effectiveDirectorySources(configured)
	} else {
		homeDir, err := s.contentInstallHomeDir()
		if err != nil {
			return false
		}
		for _, source := range configured {
			paths = append(paths, source.Path)
		}
		root := filepath.Join(homeDir, ".agents", "skills")
		if _, err := os.Stat(root); err == nil {
			paths = append(paths, root)
		}
	}
	mappedRoot, _ := resolveAgentsLock(paths)
	if mappedRoot == "" {
		return true // a fresh global install has not created the default source yet
	}
	mappedRoot, err := filepath.Abs(mappedRoot)
	if err != nil {
		return false
	}
	resolvedMappedRoot, err := filepath.EvalSymlinks(mappedRoot)
	if err != nil {
		canonicalRoot, canonicalErr := filepath.Abs(canonicalRoot)
		if errors.Is(err, os.ErrNotExist) && canonicalErr == nil && filepath.Clean(mappedRoot) == filepath.Clean(canonicalRoot) {
			return true // an explicit canonical source may be configured before its first install
		}
		return false
	}
	mappedRoot = resolvedMappedRoot
	canonicalRoot, err = filepath.Abs(canonicalRoot)
	if err != nil {
		return false
	}
	return filepath.Clean(mappedRoot) == filepath.Clean(canonicalRoot)
}

// contentRemovalProof captures the identity that exists immediately before the
// serialized CLI removal. Absence alone is never sufficient to retire proof.
type contentRemovalProof struct {
	identity contentInstallIdentity
	root     string
	lockDir  string
	linkName string
	rootInfo os.FileInfo
}

func (s *Server) prepareContentRemoval(mappedRoot, name string) (*contentRemovalProof, error) {
	records, err := readContentIdentities(s.centralDir)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	mappedRoot, err = filepath.EvalSymlinks(mappedRoot)
	if err != nil {
		return nil, err
	}
	mappedRoot, err = filepath.Abs(mappedRoot)
	if err != nil {
		return nil, err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(mappedRoot, name))
	if err != nil {
		return nil, err
	}
	var record *contentInstallIdentity
	for i := range records {
		candidate := &records[i]
		if !strings.EqualFold(candidate.Name, name) && filepath.Clean(candidate.CanonicalDir) != filepath.Clean(target) {
			continue
		}
		if record != nil {
			return nil, errContentIdentityConflict
		}
		record = candidate
	}
	if record == nil {
		return nil, nil // a normal, non-discovery installation has no proof to retire
	}
	root, lockDir, err := s.contentInstallDestination()
	if err != nil || filepath.Clean(root) != filepath.Clean(mappedRoot) || !s.contentInstallSourceMappingMatches(root) {
		return nil, errContentIdentityConflict
	}
	lock, err := source.LoadSkillLock(lockDir)
	if err != nil {
		return nil, err
	}
	ref := resolvedContentSkill{Ref: contentSkillRef{RepositoryID: record.RepositoryID, Repo: record.SourceRepo, Path: record.Path, Name: record.Name}}
	state, err := existingContentInstallState(root, lock, records, ref)
	if err != nil || state != "exact" || filepath.Clean(target) != record.CanonicalDir {
		return nil, errContentIdentityConflict
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() {
		return nil, errContentIdentityConflict
	}
	return &contentRemovalProof{identity: *record, root: root, lockDir: lockDir, linkName: name, rootInfo: rootInfo}, nil
}

func (s *Server) retireRemovedContentIdentity(proof *contentRemovalProof) error {
	if proof == nil {
		return nil
	}
	root, lockDir, err := s.contentInstallDestination()
	if err != nil || root != proof.root || lockDir != proof.lockDir || !s.contentInstallSourceMappingMatches(root) {
		return errContentIdentityConflict
	}
	rootInfo, err := os.Stat(root)
	if err != nil || !rootInfo.IsDir() || !os.SameFile(proof.rootInfo, rootInfo) {
		return errContentIdentityConflict
	}
	for _, filename := range []string{filepath.Join(root, proof.linkName), proof.identity.CanonicalDir} {
		if _, err := os.Lstat(filename); !errors.Is(err, os.ErrNotExist) {
			return errContentIdentityConflict
		}
	}
	skills, err := scanner.Scan(root)
	if err != nil {
		return err
	}
	for _, skill := range skills {
		if strings.EqualFold(skill.LinkName, proof.linkName) || strings.EqualFold(skill.LogicalName, proof.identity.Name) || strings.EqualFold(skill.FrontmatterName, proof.identity.Name) {
			return errContentIdentityConflict
		}
	}
	// The generic loader treats a planted symlink as an empty lock. That is
	// insufficient evidence for retiring an exact installation identity.
	if info, statErr := os.Lstat(filepath.Join(lockDir, ".skill-lock.json")); statErr == nil {
		if !info.Mode().IsRegular() {
			return errContentIdentityConflict
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	lock, err := source.LoadSkillLock(lockDir)
	if err != nil {
		return err
	}
	for name := range lock.Skills {
		if strings.EqualFold(name, proof.linkName) || strings.EqualFold(name, proof.identity.Name) {
			return errContentIdentityConflict
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := readContentIdentities(s.centralDir)
	if err != nil {
		return err
	}
	kept := make([]contentInstallIdentity, 0, len(records))
	found := false
	for _, record := range records {
		if sameContentIdentity(record, proof.identity) {
			found = true
			continue
		}
		kept = append(kept, record)
	}
	if !found {
		return errContentIdentityConflict
	}
	return writeContentIdentities(s.centralDir, kept)
}
