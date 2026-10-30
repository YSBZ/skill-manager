package contentgithub

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"skillmanage/internal/content"
)

type repositoryMetadata struct {
	ID            int64    `json:"id"`
	FullName      string   `json:"full_name"`
	HTMLURL       string   `json:"html_url"`
	Description   string   `json:"description"`
	Stars         int      `json:"stargazers_count"`
	CreatedAt     string   `json:"created_at"`
	DefaultBranch string   `json:"default_branch"`
	Archived      bool     `json:"archived"`
	Fork          bool     `json:"fork"`
	Topics        []string `json:"topics"`
}

type treeEvidence struct {
	Complete   bool
	SkillPaths []string
}

type repositoryInspection struct {
	Metadata repositoryMetadata
	Tree     treeEvidence
	Complete bool
	Type     string
	Scenes   []string
	Skills   []content.SkillRef
}

func decodeRepositoryMetadata(data []byte) (repositoryMetadata, error) {
	var metadata repositoryMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return repositoryMetadata{}, err
	}
	if metadata.ID <= 0 {
		return repositoryMetadata{}, errors.New("invalid repository id")
	}
	if _, _, err := repositoryNameParts(metadata.FullName); err != nil {
		return repositoryMetadata{}, err
	}
	return metadata, nil
}

func decodeTree(data []byte) (treeEvidence, error) {
	var wire struct {
		Truncated *bool `json:"truncated"`
		Tree      *[]struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return treeEvidence{}, err
	}
	if wire.Truncated == nil || wire.Tree == nil {
		return treeEvidence{}, errors.New("github tree response is incomplete")
	}
	result := treeEvidence{Complete: !*wire.Truncated}
	for _, item := range *wire.Tree {
		if item.Type == "blob" && (item.Path == "SKILL.md" || strings.HasSuffix(item.Path, "/SKILL.md")) {
			result.SkillPaths = append(result.SkillPaths, item.Path)
		}
	}
	sort.Strings(result.SkillPaths)
	return result, nil
}

func repositoryNameParts(fullName string) (string, string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || !validRepositorySegment(parts[0]) || !validRepositorySegment(parts[1]) {
		return "", "", errors.New("invalid repository full_name")
	}
	return parts[0], parts[1], nil
}

func validRepositorySegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.ContainsAny(segment, "\\\r\n\x00")
}

func repositoryAPIPath(fullName string) (string, error) {
	owner, repo, err := repositoryNameParts(fullName)
	if err != nil {
		return "", err
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo), nil
}

func (c *Client) fetchRepository(ctx context.Context, now time.Time, fullName string) (repositoryMetadata, error) {
	path, err := repositoryAPIPath(fullName)
	if err != nil {
		return repositoryMetadata{}, err
	}
	var raw json.RawMessage
	if err := c.getJSON(ctx, now, "core", path, nil, &raw); err != nil {
		return repositoryMetadata{}, err
	}
	metadata, err := decodeRepositoryMetadata(raw)
	if err != nil {
		return repositoryMetadata{}, err
	}
	if !validBranchRef(metadata.DefaultBranch) {
		return repositoryMetadata{}, errors.New("github repository metadata is incomplete")
	}
	return metadata, nil
}

func (c *Client) fetchTree(ctx context.Context, now time.Time, fullName, branch string) (treeEvidence, error) {
	base, err := repositoryAPIPath(fullName)
	if err != nil {
		return treeEvidence{}, err
	}
	if !validBranchRef(branch) {
		return treeEvidence{}, errors.New("github repository default branch is invalid")
	}
	path := base + "/git/trees/" + url.PathEscape(branch)
	var wire json.RawMessage
	if err := c.getJSON(ctx, now, "core", path, url.Values{"recursive": {"1"}}, &wire); err != nil {
		return treeEvidence{}, err
	}
	return decodeTree(wire)
}

func (c *Client) fetchSkillContents(ctx context.Context, now time.Time, fullName, path, branch string) ([]byte, error) {
	base, err := repositoryAPIPath(fullName)
	if err != nil {
		return nil, err
	}
	if !validSkillPath(path) {
		return nil, errors.New("invalid skill file path")
	}
	if !validBranchRef(branch) {
		return nil, errors.New("github repository default branch is invalid")
	}
	pathSegments := strings.Split(path, "/")
	for i, segment := range pathSegments {
		pathSegments[i] = url.PathEscape(segment)
	}
	endpoint := base + "/contents/" + strings.Join(pathSegments, "/")
	var wire struct {
		Type     string `json:"type"`
		Path     string `json:"path"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := c.getJSON(ctx, now, "core", endpoint, url.Values{"ref": {branch}}, &wire); err != nil {
		return nil, err
	}
	if wire.Type != "file" || wire.Path != path || wire.Encoding != "base64" {
		return nil, errors.New("github contents response does not match requested skill file")
	}
	return decodeSkillBase64(wire.Content)
}
