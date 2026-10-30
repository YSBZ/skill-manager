package contentgithub

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
	"skillmanage/internal/content"
)

const (
	maxSkillFilesPerRepository = 20
	maxSkillFileBytes          = 1 << 20
)

var cliSkillName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

func (c *Client) inspectRepository(ctx context.Context, now time.Time, candidate repositoryCandidate) (repositoryInspection, error) {
	inspection := repositoryInspection{Skills: []content.SkillRef{}, Scenes: []string{}}
	if candidate.ID <= 0 {
		return inspection, errors.New("invalid repository candidate id")
	}
	if _, _, err := repositoryNameParts(candidate.FullName); err != nil {
		return inspection, err
	}
	metadata, err := c.fetchRepository(ctx, now, candidate.FullName)
	if err != nil {
		return inspection, err
	}
	if metadata.ID != candidate.ID {
		return inspection, errors.New("github repository id does not match candidate")
	}
	inspection.Metadata = metadata
	if metadata.Archived {
		return inspection, nil
	}

	tree, err := c.fetchTree(ctx, now, metadata.FullName, metadata.DefaultBranch)
	if err != nil {
		return inspection, err
	}
	inspection.Tree = tree
	paths := tree.SkillPaths
	withinLimit := len(paths) <= maxSkillFilesPerRepository
	if len(paths) > maxSkillFilesPerRepository {
		paths = paths[:maxSkillFilesPerRepository]
	}

	frontmatterValid := make([]bool, 0, len(paths))
	for _, path := range paths {
		if !validSkillPath(path) {
			inspection.Skills = append(inspection.Skills, content.SkillRef{RepositoryID: metadata.ID, Repo: metadata.FullName, Path: path})
			frontmatterValid = append(frontmatterValid, false)
			continue
		}
		raw, err := c.fetchSkillContents(ctx, now, metadata.FullName, path, metadata.DefaultBranch)
		if err != nil {
			return inspection, err
		}
		ref, _, parseErr := parseSkillFileDetails(metadata.ID, metadata.FullName, path, raw)
		inspection.Skills = append(inspection.Skills, ref)
		frontmatterValid = append(frontmatterValid, parseErr == nil)
	}

	uniqueNames := true
	nameCounts := make(map[string]int, len(inspection.Skills))
	for i, ref := range inspection.Skills {
		if frontmatterValid[i] {
			nameCounts[strings.ToLower(ref.Name)]++
		}
	}
	for _, count := range nameCounts {
		if count > 1 {
			uniqueNames = false
			break
		}
	}
	inspection.Complete = tree.Complete && withinLimit && uniqueNames && allTrue(frontmatterValid)
	for i := range inspection.Skills {
		inspection.Skills[i].Verified = false
	}
	if inspection.Complete {
		for i := range inspection.Skills {
			inspection.Skills[i].Verified = true
		}
	}
	inspection.Type, inspection.Scenes = classifyRepository(metadata, inspection.Skills)
	return inspection, nil
}

func validSkillPath(path string) bool {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return parts[len(parts)-1] == "SKILL.md"
}

func validBranchRef(branch string) bool {
	if branch == "" || strings.ContainsAny(branch, "\\\r\n\x00?#") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func decodeSkillBase64(encoded string) ([]byte, error) {
	decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	decoded, err := io.ReadAll(io.LimitReader(decoder, maxSkillFileBytes+1))
	if err != nil {
		return nil, errors.New("github skill file content is invalid base64")
	}
	if len(decoded) > maxSkillFileBytes {
		return nil, errors.New("github skill file exceeds 1 MiB limit")
	}
	return decoded, nil
}

func parseSkillFile(repoID int64, repo, path string, raw []byte) (content.SkillRef, error) {
	ref, _, err := parseSkillFileDetails(repoID, repo, path, raw)
	return ref, err
}

func parseSkillFileDetails(repoID int64, repo, path string, raw []byte) (content.SkillRef, string, error) {
	ref := content.SkillRef{RepositoryID: repoID, Repo: repo, Path: path}
	if repoID <= 0 {
		return ref, "", errors.New("invalid repository identity")
	}
	if _, _, err := repositoryNameParts(repo); err != nil {
		return ref, "", err
	}
	if !validSkillPath(path) {
		return ref, "", errors.New("invalid skill file path")
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return ref, "", errors.New("skill frontmatter missing")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" || lines[i] == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return ref, "", errors.New("skill frontmatter unterminated")
	}
	var frontmatter struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &frontmatter); err != nil {
		return ref, "", errors.New("invalid skill frontmatter")
	}
	ref.Name = strings.TrimSpace(frontmatter.Name)
	if !cliSkillName.MatchString(ref.Name) || ref.Name == "." || ref.Name == ".." || strings.TrimSpace(frontmatter.Description) == "" {
		return ref, "", errors.New("skill name or description is invalid")
	}
	ref.Verified = true
	return ref, strings.TrimSpace(frontmatter.Description), nil
}

func allTrue(values []bool) bool {
	for _, value := range values {
		if !value {
			return false
		}
	}
	return true
}

func classifyRepository(metadata repositoryMetadata, skills []content.SkillRef) (string, []string) {
	textParts := []string{metadata.Description}
	textParts = append(textParts, metadata.Topics...)
	for _, skill := range skills {
		textParts = append(textParts, skill.Name)
	}
	text := strings.ToLower(strings.Join(textParts, " "))
	topics := make(map[string]bool, len(metadata.Topics))
	for _, topic := range metadata.Topics {
		topics[strings.ToLower(topic)] = true
	}

	typeName := ""
	if len(skills) > 0 {
		typeName = "skill"
	} else if topicMatches(topics, "agent-skills", "claude-skills", "codex-skills", "agent-workflow", "agent-framework") ||
		textMatches(text, "agent workflow", "agent framework", "autonomous agent") {
		typeName = "agent"
	} else if topicMatches(topics, "mcp-server", "developer-tools", "cli-tool", "cli-tools", "toolkit") ||
		textMatches(text, "mcp server", "developer tool", "cli tool", "toolkit") {
		typeName = "tool"
	}

	scenes := make([]string, 0, 5)
	for _, scene := range []struct {
		name     string
		keywords []string
	}{
		{name: "development", keywords: []string{"development", "developer", "programming", "programmer", "coding", "software engineering"}},
		{name: "design", keywords: []string{"design", "designer", "ui", "ux"}},
		{name: "testing", keywords: []string{"testing", "test", "quality assurance", "qa"}},
		{name: "documentation", keywords: []string{"documentation", "technical writing", "docs"}},
		{name: "agent-workflow", keywords: []string{"agent workflow", "agent framework", "autonomous agent"}},
	} {
		if textMatches(text, scene.keywords...) {
			scenes = append(scenes, scene.name)
		}
	}
	return typeName, scenes
}

func topicMatches(topics map[string]bool, values ...string) bool {
	for _, value := range values {
		if topics[value] {
			return true
		}
	}
	return false
}

func textMatches(text string, values ...string) bool {
	normalized := " " + normalizeClassificationText(text) + " "
	for _, value := range values {
		phrase := " " + normalizeClassificationText(value) + " "
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func normalizeClassificationText(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsNumber(char)
	})
	return strings.Join(words, " ")
}
