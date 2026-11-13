package skillcheck

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

const (
	maxSkillBytes = 1 << 20
	maxDepth      = 32
	maxNodes      = 128
)

var (
	errNotRegular = errors.New("not a regular file")
	errTooLarge   = errors.New("file exceeds the 1 MiB read limit")

	skillCallPattern     = regexp.MustCompile("(?i)^(?:please\\s+)?Call\\s+the\\s+Skill\\s+tool\\s+with\\s+[\"'`]([A-Za-z0-9][A-Za-z0-9._-]*(?::[A-Za-z0-9][A-Za-z0-9._-]*)?)[\"'`]")
	requiredSkillPattern = regexp.MustCompile(`(?i)^REQUIRED\s+SUB-SKILL\s*:\s*Use\s+([A-Za-z0-9][A-Za-z0-9._-]*(?::[A-Za-z0-9][A-Za-z0-9._-]*)?)`)
	clientMentionPattern = regexp.MustCompile(`(?i)\b(claude\s+code|codex|pi)\b`)
)

// Report is the read-only local readiness summary for one skill entry.
type Report struct {
	State         string       `json:"state"`
	Path          PathInfo     `json:"path"`
	Dependencies  []Dependency `json:"dependencies"`
	Compatibility string       `json:"compatibility"`
	AllowedTools  []string     `json:"allowedTools"`
	Findings      []Finding    `json:"findings"`
}

// PathInfo describes the user-visible entry and its resolved skill directory.
type PathInfo struct {
	Entry      string `json:"entry"`
	LinkTarget string `json:"linkTarget"`
	Resolved   string `json:"resolved"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
}

// Dependency describes one explicitly required skill in the current scope.
type Dependency struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Source   string `json:"source"`
	Evidence string `json:"evidence"`
}

// Finding is a local issue or a request for the user to review compatibility.
type Finding struct {
	Code       string `json:"code"`
	Level      string `json:"level"`
	Message    string `json:"message"`
	Evidence   string `json:"evidence"`
	Dependency string `json:"dependency"`
}

type metadata struct {
	compatibility string
	allowedTools  []string
}

type skillDoc struct {
	location    skillLocation
	metadata    metadata
	metadataErr error
	body        string
}

type skillLocation struct {
	info      PathInfo
	contentAt string
}

type depRef struct {
	name          string
	evidence      string
	position      int
	usesSkillTool bool
}

type inspector struct {
	report    *Report
	client    string
	available map[string]string
	visited   map[string]bool
	active    map[string]bool
	depIndex  map[string]int
	nodes     int
}

// ReadContent reads a regular file without following it as a special file or
// consuming more than 1 MiB. It is exported so detail views can use the same
// bounded file read as Inspect.
func ReadContent(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", path, errNotRegular)
	}
	if info.Size() > maxSkillBytes {
		return "", fmt.Errorf("%s: %w", path, errTooLarge)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Recheck after opening so a changed path cannot make the read unbounded.
	openedInfo, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !openedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %w", path, errNotRegular)
	}
	if openedInfo.Size() > maxSkillBytes {
		return "", fmt.Errorf("%s: %w", path, errTooLarge)
	}

	content, err := io.ReadAll(io.LimitReader(file, maxSkillBytes+1))
	if err != nil {
		return "", err
	}
	if len(content) > maxSkillBytes {
		return "", fmt.Errorf("%s: %w", path, errTooLarge)
	}
	return string(content), nil
}

// Inspect reports path health, explicit skill dependencies, and compatibility
// notes. It does not execute the skill or inspect system commands or MCP state.
func Inspect(path, client string, available map[string]string) Report {
	report := Report{
		Path:         PathInfo{Entry: path},
		Dependencies: []Dependency{},
		AllowedTools: []string{},
		Findings:     []Finding{},
	}
	check := &inspector{
		report:    &report,
		client:    normalizeClient(client),
		available: available,
		visited:   make(map[string]bool),
		active:    make(map[string]bool),
		depIndex:  make(map[string]int),
		nodes:     1,
	}

	location, finding := resolveSkill(path)
	report.Path = location.info
	if finding != nil {
		check.addFinding(*finding)
		check.setState()
		return report
	}

	doc, err := readSkill(location)
	if err != nil {
		report.Path.State = "unreadable"
		check.addFinding(readFailureFinding(location, err, ""))
		check.setState()
		return report
	}
	check.visit(doc, "", 0)
	check.setState()
	return report
}

func (c *inspector) visit(doc *skillDoc, viaDependency string, depth int) {
	canonical := doc.location.info.Resolved
	if canonical == "" {
		canonical = doc.location.info.Entry
	}
	c.visited[canonical] = true
	c.active[canonical] = true
	defer delete(c.active, canonical)

	if doc.metadataErr != nil {
		c.addFinding(Finding{
			Code:       "invalid_frontmatter",
			Level:      "error",
			Message:    "技能元数据解析失败；兼容性和预批准工具信息可能不完整。",
			Evidence:   doc.location.contentAt + ": " + doc.metadataErr.Error(),
			Dependency: viaDependency,
		})
	}
	if viaDependency == "" {
		c.report.Compatibility = doc.metadata.compatibility
		c.report.AllowedTools = append([]string{}, doc.metadata.allowedTools...)
	}
	if declared, ok := explicitSingleClientOnly(doc.metadata.compatibility); ok && declared != c.client {
		c.addFinding(Finding{
			Code:       "compatibility_review",
			Level:      "warning",
			Message:    fmt.Sprintf("该技能声明仅兼容 %s，而当前客户端是 %s。请确认工具映射或适配后再使用；这项声明不代表绝对不兼容。", displayClient(declared), displayClient(c.client)),
			Evidence:   doc.location.contentAt + ": compatibility: " + doc.metadata.compatibility,
			Dependency: viaDependency,
		})
	}

	refs := dependencyRefs(doc.body)
	for _, ref := range refs {
		evidence := doc.location.contentAt + ": " + ref.evidence
		availablePath, present := c.available[ref.name]
		state := "missing"
		if present {
			state = "present"
		}
		if _, exists := c.depIndex[ref.name]; !exists {
			c.depIndex[ref.name] = len(c.report.Dependencies)
			c.report.Dependencies = append(c.report.Dependencies, Dependency{
				Name:     ref.name,
				State:    state,
				Source:   availablePath,
				Evidence: evidence,
			})
		}
		if !present {
			c.addFinding(Finding{
				Code:       "dependency_missing",
				Level:      "error",
				Message:    fmt.Sprintf("当前客户端范围内找不到必需技能 %q。", ref.name),
				Evidence:   evidence,
				Dependency: ref.name,
			})
			continue
		}

		childLocation, childProblem := resolveSkill(availablePath)
		if childProblem != nil {
			c.addFinding(Finding{
				Code:       "dependency_unavailable",
				Level:      "error",
				Message:    fmt.Sprintf("已发现必需技能 %q，但无法检查：%s", ref.name, childProblem.Message),
				Evidence:   evidence + "; entry: " + availablePath,
				Dependency: ref.name,
			})
			continue
		}

		childPath := childLocation.info.Resolved
		if childPath == "" {
			childPath = childLocation.info.Entry
		}
		if c.active[childPath] {
			c.addFinding(Finding{
				Code:       "dependency_cycle",
				Level:      "warning",
				Message:    fmt.Sprintf("必需技能 %q 形成循环依赖，请人工确认。", ref.name),
				Evidence:   evidence + "; resolves to " + childPath,
				Dependency: ref.name,
			})
			continue
		}
		if c.visited[childPath] {
			continue
		}
		childDepth := depth + 1
		if childDepth > maxDepth {
			c.truncated(ref, evidence, fmt.Sprintf("依赖深度超过 %d 层", maxDepth))
			continue
		}
		if c.nodes >= maxNodes {
			c.truncated(ref, evidence, fmt.Sprintf("依赖图超过 %d 个技能", maxNodes))
			continue
		}

		c.visited[childPath] = true
		c.nodes++
		childDoc, err := readSkill(childLocation)
		if err != nil {
			c.addFinding(readFailureFinding(childLocation, err, ref.name))
			continue
		}
		c.visit(childDoc, ref.name, childDepth)
	}

	for _, ref := range refs {
		if ref.usesSkillTool && c.client != "cc" {
			c.addFinding(Finding{
				Code:       "skill_tool_review",
				Level:      "warning",
				Message:    fmt.Sprintf("该技能要求调用 Skill 工具，而当前客户端是 %s。请确认工具映射或适配此步骤后再使用。", displayClient(c.client)),
				Evidence:   doc.location.contentAt + ": " + ref.evidence,
				Dependency: ref.name,
			})
		}
	}
}

func (c *inspector) truncated(ref depRef, evidence, reason string) {
	c.addFinding(Finding{
		Code:       "dependency_truncated",
		Level:      "warning",
		Message:    "依赖分析已停止：" + reason + "。",
		Evidence:   evidence,
		Dependency: ref.name,
	})
}

func (c *inspector) addFinding(finding Finding) {
	for _, existing := range c.report.Findings {
		if existing.Code == finding.Code && existing.Dependency == finding.Dependency && existing.Evidence == finding.Evidence {
			return
		}
	}
	c.report.Findings = append(c.report.Findings, finding)
}

func (c *inspector) setState() {
	state := "checked"
	for _, finding := range c.report.Findings {
		if finding.Level == "error" {
			state = "issues"
			break
		}
		if finding.Level == "warning" {
			state = "review"
		}
	}
	c.report.State = state
}

func resolveSkill(entry string) (skillLocation, *Finding) {
	location := skillLocation{info: PathInfo{Entry: entry}}
	if strings.TrimSpace(entry) == "" {
		location.info.State = "missing_skill"
		return location, &Finding{
			Code:     "missing_skill",
			Level:    "error",
			Message:  "技能入口路径为空。",
			Evidence: entry,
		}
	}

	lstat, err := os.Lstat(entry)
	if err != nil {
		if os.IsNotExist(err) {
			location.info.State = "missing_skill"
			return location, &Finding{Code: "missing_skill", Level: "error", Message: "技能入口不存在。", Evidence: entry}
		}
		location.info.State = "unreadable"
		return location, &Finding{Code: "path_unreadable", Level: "error", Message: "无法检查技能入口。", Evidence: entry + ": " + err.Error()}
	}
	if lstat.Mode()&os.ModeSymlink != 0 {
		location.info.Kind = "symlink"
	}
	if target, targetErr := os.Readlink(entry); targetErr == nil {
		location.info.Kind = "symlink"
		location.info.LinkTarget = target
	}

	absolute, err := filepath.Abs(entry)
	if err != nil {
		location.info.State = "unreadable"
		return location, &Finding{Code: "path_unreadable", Level: "error", Message: "无法将技能入口解析为绝对路径。", Evidence: entry + ": " + err.Error()}
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if os.IsPermission(err) {
			location.info.State = "unreadable"
			return location, &Finding{Code: "path_unreadable", Level: "error", Message: "因权限不足，无法解析技能入口。", Evidence: entry + ": " + err.Error()}
		}
		if location.info.Kind == "symlink" || isLinkLoop(err) {
			location.info.State = "broken"
			return location, &Finding{Code: "broken_path", Level: "error", Message: "技能入口软链断开或形成循环。", Evidence: entry + ": " + err.Error()}
		}
		if os.IsNotExist(err) {
			location.info.State = "missing_skill"
			return location, &Finding{Code: "missing_skill", Level: "error", Message: "技能入口未指向现存目录。", Evidence: entry + ": " + err.Error()}
		}
		location.info.State = "unreadable"
		return location, &Finding{Code: "path_unreadable", Level: "error", Message: "无法解析技能入口。", Evidence: entry + ": " + err.Error()}
	}
	resolved = filepath.Clean(resolved)
	location.info.Resolved = resolved
	if location.info.Kind == "" {
		// Compare against the resolved parent so aliases in an ancestor (such
		// as macOS /var -> /private/var) do not turn a real directory into a
		// symlink entry. A junction at the entry itself resolves differently.
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute))
		if parentErr == nil {
			expected := filepath.Join(resolvedParent, filepath.Base(absolute))
			if !samePath(expected, resolved) {
				location.info.Kind = "symlink"
				if target, targetErr := os.Readlink(entry); targetErr == nil {
					location.info.LinkTarget = target
				} else {
					location.info.LinkTarget = resolved
				}
			}
		}
	}
	if location.info.Kind == "" && lstat.IsDir() {
		location.info.Kind = "directory"
	}

	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			location.info.State = "missing_skill"
			return location, &Finding{Code: "missing_skill", Level: "error", Message: "解析后的技能目录不存在。", Evidence: resolved + ": " + err.Error()}
		}
		location.info.State = "unreadable"
		return location, &Finding{Code: "path_unreadable", Level: "error", Message: "无法检查解析后的技能目录。", Evidence: resolved + ": " + err.Error()}
	}
	if !resolvedInfo.IsDir() {
		location.info.State = "missing_skill"
		return location, &Finding{Code: "missing_skill", Level: "error", Message: "技能入口未指向目录。", Evidence: resolved}
	}

	location.contentAt = filepath.Join(resolved, "SKILL.md")
	if _, err := os.Lstat(location.contentAt); err != nil {
		if os.IsNotExist(err) {
			location.info.State = "missing_skill"
			return location, &Finding{Code: "missing_skill", Level: "error", Message: "技能目录中没有 SKILL.md 文件。", Evidence: location.contentAt}
		}
		location.info.State = "unreadable"
		return location, &Finding{Code: "path_unreadable", Level: "error", Message: "无法检查 SKILL.md。", Evidence: location.contentAt + ": " + err.Error()}
	}
	location.info.State = "ok"
	return location, nil
}

func readSkill(location skillLocation) (*skillDoc, error) {
	content, err := ReadContent(location.contentAt)
	if err != nil {
		return nil, err
	}
	meta, body, metadataErr := parseFrontMatter(content)
	return &skillDoc{location: location, metadata: meta, metadataErr: metadataErr, body: body}, nil
}

func readFailureFinding(location skillLocation, err error, dependency string) Finding {
	code := "path_unreadable"
	message := "无法读取 SKILL.md。"
	if errors.Is(err, errTooLarge) {
		code = "skill_too_large"
		message = "SKILL.md 超过 1 MiB 读取上限。"
	} else if errors.Is(err, errNotRegular) {
		code = "skill_not_regular"
		message = "SKILL.md 不是普通文件。"
	}
	return Finding{
		Code:       code,
		Level:      "error",
		Message:    message,
		Evidence:   location.contentAt + ": " + err.Error(),
		Dependency: dependency,
	}
}

func parseFrontMatter(content string) (metadata, string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if trimLineEnding(lines[0]) != "---" {
		return metadata{allowedTools: []string{}}, content, nil
	}

	end := -1
	for i := 1; i < len(lines); i++ {
		if trimLineEnding(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return metadata{}, "", errors.New("frontmatter has no closing --- delimiter")
	}
	body := strings.Join(lines[end+1:], "\n")
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fields); err != nil {
		return metadata{}, body, err
	}

	meta := metadata{allowedTools: []string{}}
	if value, ok := fields["compatibility"]; ok && value != nil {
		text, ok := value.(string)
		if !ok {
			return meta, body, errors.New("frontmatter compatibility must be a string")
		}
		meta.compatibility = text
	}
	if value, ok := fields["allowed-tools"]; ok && value != nil {
		tools, err := parseAllowedTools(value)
		if err != nil {
			return meta, body, err
		}
		meta.allowedTools = tools
	}
	return meta, body, nil
}

func parseAllowedTools(value any) ([]string, error) {
	tools := []string{}
	appendTool := func(value any) error {
		tool, ok := value.(string)
		if !ok {
			return errors.New("frontmatter allowed-tools entries must be strings")
		}
		if tool = strings.TrimSpace(tool); tool != "" {
			tools = append(tools, tool)
		}
		return nil
	}
	switch value := value.(type) {
	case string:
		if err := appendTool(value); err != nil {
			return nil, err
		}
	case []any:
		for _, item := range value {
			if err := appendTool(item); err != nil {
				return nil, err
			}
		}
	default:
		return nil, errors.New("frontmatter allowed-tools must be a string or list of strings")
	}
	return tools, nil
}

func dependencyRefs(body string) []depRef {
	source := []byte(body)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))
	refs := make([]depRef, 0)
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindBlockquote, ast.KindCodeBlock, ast.KindFencedCodeBlock, ast.KindHTMLBlock:
			return ast.WalkSkipChildren, nil
		case ast.KindParagraph, ast.KindTextBlock:
			// CommonMark owns block/container syntax; regexes below only identify
			// the explicit dependency instruction domain. TextBlock covers tight
			// lists, whose real instructions remain discoverable.
			for i := 0; i < node.Lines().Len(); i++ {
				segment := node.Lines().At(i)
				cleaned := cleanDirectiveLine(string(segment.Value(source)))
				start := strings.LastIndexByte(body[:segment.Start], '\n') + 1
				end := segment.Stop
				if newline := strings.IndexByte(body[start:], '\n'); newline >= 0 {
					end = start + newline
				}
				rawLine := body[start:end]
				position := start + len(rawLine) - len(strings.TrimLeft(rawLine, " \t\r"))
				if match := skillCallPattern.FindStringSubmatch(cleaned); len(match) > 1 {
					refs = append(refs, depRef{name: match[1], evidence: strings.TrimSpace(rawLine), position: position, usesSkillTool: true})
				}
				if match := requiredSkillPattern.FindStringSubmatch(cleaned); len(match) > 1 {
					refs = append(refs, depRef{name: match[1], evidence: strings.TrimSpace(rawLine), position: position})
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].position < refs[j].position })
	return refs
}

func cleanDirectiveLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.ReplaceAll(line, "**", "")
	line = strings.Replace(line, "__REQUIRED SUB-SKILL:__", "REQUIRED SUB-SKILL:", 1)
	line = strings.Replace(line, "__REQUIRED SUB-SKILL__:", "REQUIRED SUB-SKILL:", 1)
	if len(line) >= 2 && ((line[0] == '*' && line[len(line)-1] == '*') || (line[0] == '_' && line[len(line)-1] == '_')) {
		line = line[1 : len(line)-1]
	}
	return strings.TrimSpace(line)
}

func explicitSingleClientOnly(compatibility string) (string, bool) {
	if strings.TrimSpace(compatibility) == "" {
		return "", false
	}
	mentions := clientMentionPattern.FindAllStringSubmatchIndex(compatibility, -1)
	clients := make(map[string]bool)
	for _, match := range mentions {
		clients[clientFromName(compatibility[match[2]:match[3]])] = true
	}
	if len(clients) != 1 {
		return "", false
	}
	for _, match := range mentions {
		client := clientFromName(compatibility[match[2]:match[3]])
		before := strings.ToLower(compatibility[:match[2]])
		after := strings.ToLower(compatibility[match[3]:])
		if hasOnlyBeforeClient(before) || hasOnlyAfterClient(after) {
			return client, true
		}
	}
	return "", false
}

func hasOnlyAfterClient(after string) bool {
	after = strings.TrimLeft(after, " \t,;:.-")
	if !strings.HasPrefix(after, "only") {
		return false
	}
	rest := strings.TrimLeft(after[len("only"):], " \t")
	return rest == "" || strings.ContainsRune(".,;:!?\n\r", rune(rest[0]))
}

func hasOnlyBeforeClient(before string) bool {
	if len(before) > 64 {
		before = before[len(before)-64:]
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`\bonly\s+(?:compatible|works?|supported|designed|intended|available)(?:\s+(?:for|with|on|in|to))?\s*$`),
		regexp.MustCompile(`\bonly\s+(?:for|with|on|in)\s*$`),
		regexp.MustCompile(`\b(?:compatible|works?|supported|designed|intended|available)\s+(?:for|with|on|in|to)\s+only\s*$`),
		regexp.MustCompile(`\bdesigned\s+only\s+for\s*$`),
	}
	for _, pattern := range patterns {
		if pattern.MatchString(before) {
			return true
		}
	}
	return false
}

func clientFromName(name string) string {
	switch strings.ToLower(strings.Join(strings.Fields(name), " ")) {
	case "claude code":
		return "cc"
	case "codex":
		return "codex"
	case "pi":
		return "pi"
	default:
		return "unknown"
	}
}

func normalizeClient(client string) string {
	switch strings.ToLower(strings.TrimSpace(client)) {
	case "cc", "codex", "pi":
		return strings.ToLower(strings.TrimSpace(client))
	default:
		return "unknown"
	}
}

func displayClient(client string) string {
	switch client {
	case "cc":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "pi":
		return "pi"
	default:
		return "unknown client"
	}
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func isLinkLoop(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "too many levels of symbolic links") || strings.Contains(text, "too many symbolic links") || strings.Contains(text, "too many links")
}

func trimLineEnding(line string) string {
	return strings.TrimSuffix(line, "\r")
}
