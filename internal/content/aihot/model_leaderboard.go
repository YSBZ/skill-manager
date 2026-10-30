package aihot

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"skillmanage/internal/content"
)

const (
	modelLeaderboardSourceNamePrefix = "aihot:model-leaderboard:"
	modelLeaderboardCurrency         = "CNY"
	modelLeaderboardPriceUnit        = "人民币 / 百万 Token"
	maxLeaderboardRows               = 1000
	maxLeaderboardNotes              = 40
	maxLeaderboardNoteLength         = 2000
)

var (
	modelCountPattern     = regexp.MustCompile(`当前展示\s*([0-9]+)\s*个模型`)
	benchmarkCountPattern = regexp.MustCompile(`([0-9]+)\s*项评测`)
	coveragePattern       = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)
	modelSlugPattern      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type modelLeaderboardFetchResult struct {
	Snapshot     content.ModelLeaderboard
	ETag         string
	CacheControl string
	LastModified string
	NotModified  bool
}

func (c *Client) FetchModelLeaderboard(ctx context.Context, category, etag string) (modelLeaderboardFetchResult, error) {
	sourceURL, err := content.ModelLeaderboardSourceURL(category)
	if err != nil {
		return modelLeaderboardFetchResult{}, errInvalidQuery
	}
	parsedURL, err := url.Parse(sourceURL)
	if err != nil {
		return modelLeaderboardFetchResult{}, errInvalidQuery
	}
	status, headers, body, err := c.getHTML(ctx, parsedURL.Path, nil, etag)
	if err != nil {
		return modelLeaderboardFetchResult{}, err
	}
	out := modelLeaderboardFetchResult{
		ETag: headers.Get("ETag"), CacheControl: headers.Get("Cache-Control"), LastModified: headers.Get("Last-Modified"),
	}
	if status == 304 {
		out.NotModified = true
		return out, nil
	}
	if status != http.StatusOK {
		return modelLeaderboardFetchResult{}, &upstreamError{Status: status, RetryAfter: headers.Get("Retry-After")}
	}
	snapshot, err := parseModelLeaderboard(category, body)
	if err != nil {
		return modelLeaderboardFetchResult{}, errIncompletePage
	}
	out.Snapshot = snapshot
	return out, nil
}

func parseModelLeaderboard(category string, body []byte) (content.ModelLeaderboard, error) {
	category, err := content.NormalizeModelLeaderboardCategory(category)
	if err != nil || len(body) == 0 || len(body) > maxResponseBody {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	title := normalizedHTMLText(firstDescendant(doc, "title"))
	updatedLabel := leaderboardUpdatedLabel(doc)
	priceBasisNote, ok := leaderboardPriceBasisNote(doc)
	if !ok {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	table, err := leaderboardTable(doc)
	if err != nil || title == "" || updatedLabel == "" {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	caption := normalizedHTMLText(firstDescendant(table, "caption"))
	countMatch := modelCountPattern.FindStringSubmatch(caption)
	if len(countMatch) != 2 {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	declaredCount, err := strconv.Atoi(countMatch[1])
	if err != nil || declaredCount < 1 || declaredCount > maxLeaderboardRows {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	if err := validateLeaderboardHeaders(table); err != nil {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	rows := make([]*html.Node, 0, declaredCount)
	for _, row := range descendants(table, "tr") {
		if attr(row, "data-slug") != "" {
			rows = append(rows, row)
		}
	}
	if len(rows) != declaredCount || len(rows) == 0 {
		return content.ModelLeaderboard{}, errIncompletePage
	}
	sourceURL, _ := content.ModelLeaderboardSourceURL(category)
	priceNotes := leaderboardPriceNotes(doc, table)
	priceNotes = includeRequiredPriceBasis(priceNotes, priceBasisNote)
	snapshot := content.ModelLeaderboard{
		Category: category, Title: title, SourceURL: sourceURL, UpdatedLabel: updatedLabel,
		Currency: modelLeaderboardCurrency, PriceUnit: modelLeaderboardPriceUnit,
		Entries: make([]content.ModelLeaderboardEntry, 0, len(rows)), PriceNotes: priceNotes,
	}
	seenIDs := make(map[string]bool, len(rows))
	seenRanks := make(map[int]bool, len(rows))
	for _, row := range rows {
		entry, err := parseModelLeaderboardEntry(row)
		if err != nil || seenIDs[entry.ID] || seenRanks[entry.Rank] {
			return content.ModelLeaderboard{}, errIncompletePage
		}
		seenIDs[entry.ID] = true
		seenRanks[entry.Rank] = true
		snapshot.Entries = append(snapshot.Entries, entry)
	}
	return snapshot, nil
}

func leaderboardTable(doc *html.Node) (*html.Node, error) {
	var matches []*html.Node
	for _, table := range descendants(doc, "table") {
		for _, row := range descendants(table, "tr") {
			if attr(row, "data-slug") != "" {
				matches = append(matches, table)
				break
			}
		}
	}
	if len(matches) != 1 || firstDescendant(matches[0], "caption") == nil || firstDescendant(matches[0], "thead") == nil {
		return nil, fmt.Errorf("leaderboard table missing or ambiguous")
	}
	return matches[0], nil
}

func validateLeaderboardHeaders(table *html.Node) error {
	head := firstDescendant(table, "thead")
	if head == nil {
		return fmt.Errorf("leaderboard header missing")
	}
	var headers []string
	for _, th := range descendants(head, "th") {
		headers = append(headers, normalizedHTMLText(th))
	}
	want := []string{"名次", "模型", "上线日期", "评测覆盖", "缓存价格", "输入价格", "输出价格", "AIHOT 评分"}
	if len(headers) != len(want) {
		return fmt.Errorf("unexpected leaderboard header count")
	}
	for i, label := range want {
		if !strings.Contains(headers[i], label) {
			return fmt.Errorf("unexpected leaderboard header %d", i)
		}
	}
	return nil
}

func parseModelLeaderboardEntry(row *html.Node) (content.ModelLeaderboardEntry, error) {
	slug := attr(row, "data-slug")
	if !modelSlugPattern.MatchString(slug) {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid model slug")
	}
	cells := directChildren(row, "td")
	if len(cells) != 8 {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("unexpected model column count")
	}
	rank, err := strconv.Atoi(normalizedHTMLText(cells[0]))
	if err != nil || rank < 1 || rank > maxLeaderboardRows {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid model rank")
	}
	name := normalizedHTMLText(firstDescendant(cells[1], "strong"))
	provider := ""
	if small := firstDescendant(cells[1], "small"); small != nil {
		provider = normalizedHTMLText(small)
	}
	if name == "" || provider == "" || !hasModelDetailLink(cells[1], slug) {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid model identity")
	}
	releasedOn := ""
	if date := firstDescendant(cells[2], "time"); date != nil {
		releasedOn = strings.TrimSpace(attr(date, "datetime"))
	}
	if releasedOn == "" || !validModelDate(releasedOn) {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid model release date")
	}
	var benchmarkCount *int
	if match := benchmarkCountPattern.FindStringSubmatch(normalizedHTMLText(cells[3])); len(match) == 2 {
		value, err := strconv.Atoi(match[1])
		if err != nil || value < 1 || value > 10000 {
			return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid benchmark count")
		}
		benchmarkCount = &value
	}
	var coveragePercent *float64
	if match := coveragePattern.FindStringSubmatch(normalizedHTMLText(cells[3])); len(match) == 2 {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
			return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid evaluation coverage")
		}
		coveragePercent = &value
	}
	scoreText := normalizedHTMLText(firstDescendant(cells[7], "strong"))
	score, err := strconv.ParseFloat(scoreText, 64)
	if err != nil || math.IsNaN(score) || math.IsInf(score, 0) || score < -100 || score > 200 {
		return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid AIHOT score")
	}
	prices := []string{normalizedHTMLText(cells[4]), normalizedHTMLText(cells[5]), normalizedHTMLText(cells[6])}
	for _, price := range prices {
		if price == "" || len(price) > 200 {
			return content.ModelLeaderboardEntry{}, fmt.Errorf("invalid source price")
		}
	}
	entry := content.ModelLeaderboardEntry{
		ID: slug, Rank: rank, Name: name, Provider: provider, ReleasedOn: releasedOn,
		CoveragePercent: coveragePercent, BenchmarkCount: benchmarkCount, Score: score,
		CachePrice: prices[0], InputPrice: prices[1], OutputPrice: prices[2],
		URL:            "https://aihot.news/leaderboard/" + slug,
		OpenWeightsURL: openWeightsURL(cells[1]),
	}
	return entry, nil
}

func hasModelDetailLink(cell *html.Node, slug string) bool {
	for _, anchor := range descendants(cell, "a") {
		href := strings.TrimSpace(attr(anchor, "href"))
		parsed, err := url.Parse(href)
		if err != nil || (parsed.Scheme != "" && parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Fragment != "" {
			continue
		}
		if parsed.Host != "" && !strings.EqualFold(parsed.Host, "aihot.news") {
			continue
		}
		if parsed.Path == "/leaderboard/"+slug {
			return true
		}
	}
	return false
}

func openWeightsURL(cell *html.Node) string {
	for _, anchor := range descendants(cell, "a") {
		parsed, err := url.Parse(strings.TrimSpace(attr(anchor, "href")))
		if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "huggingface.co" || host == "hf.co" {
			return parsed.String()
		}
	}
	return ""
}

func leaderboardUpdatedLabel(doc *html.Node) string {
	for _, paragraph := range descendants(doc, "p") {
		classes := strings.Fields(attr(paragraph, "class"))
		isNumericLabel := false
		for _, class := range classes {
			if class == "num" {
				isNumericLabel = true
				break
			}
		}
		label := normalizedHTMLText(paragraph)
		if isNumericLabel && strings.Contains(label, "评测") && strings.Contains(label, "更新") {
			return label
		}
	}
	return ""
}

func leaderboardPriceBasisNote(doc *html.Node) (string, bool) {
	for _, heading := range descendants(doc, "h2") {
		if normalizedHTMLText(heading) != "关于价格" {
			continue
		}
		section := ancestorElement(heading, "section")
		if section == nil {
			return "", false
		}
		paragraph := firstParagraphAfter(section, heading)
		if paragraph == nil {
			return "", false
		}
		note := normalizedHTMLText(paragraph)
		lower := strings.ToLower(note)
		hasCurrency := strings.Contains(note, "人民币") || strings.Contains(note, "¥") || strings.Contains(lower, "cny")
		if note == "" || len(note) > maxLeaderboardNoteLength ||
			!strings.Contains(note, "价格") || !strings.Contains(note, "百万") || !strings.Contains(lower, "token") || !hasCurrency {
			return "", false
		}
		return note, true
	}
	return "", false
}

func includeRequiredPriceBasis(notes []string, basis string) []string {
	for _, note := range notes {
		if note == basis {
			return notes
		}
	}
	if notes == nil {
		notes = []string{}
	}
	if len(notes) >= maxLeaderboardNotes {
		notes = notes[:maxLeaderboardNotes-1]
	}
	return append(notes, basis)
}

func ancestorElement(node *html.Node, tag string) *html.Node {
	for current := node.Parent; current != nil; current = current.Parent {
		if current.Type == html.ElementNode && current.Data == tag {
			return current
		}
	}
	return nil
}

func firstParagraphAfter(section, heading *html.Node) *html.Node {
	foundHeading := false
	var paragraph *html.Node
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if paragraph != nil {
			return
		}
		if current == heading {
			foundHeading = true
			return
		}
		if foundHeading && current.Type == html.ElementNode && current.Data == "p" {
			paragraph = current
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
			if paragraph != nil {
				return
			}
		}
	}
	visit(section)
	return paragraph
}

func leaderboardPriceNotes(doc, table *html.Node) []string {
	var notes []string
	seen := make(map[string]bool)
	for _, paragraph := range descendants(doc, "p") {
		if isDescendantOf(paragraph, table) {
			continue
		}
		text := normalizedHTMLText(paragraph)
		if text == "" || len(text) > maxLeaderboardNoteLength ||
			!(strings.Contains(text, "价格") || strings.Contains(text, "报价") || strings.Contains(text, "缓存命中")) || seen[text] {
			continue
		}
		seen[text] = true
		notes = append(notes, text)
		if len(notes) >= maxLeaderboardNotes {
			break
		}
	}
	return notes
}

func isDescendantOf(node, ancestor *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if current == ancestor {
			return true
		}
	}
	return false
}

func validModelDate(value string) bool {
	parsed, err := time.Parse(time.DateOnly, value)
	return err == nil && parsed.Format(time.DateOnly) == value
}

func firstDescendant(node *html.Node, tag string) *html.Node {
	for _, child := range descendants(node, tag) {
		return child
	}
	return nil
}

func descendants(node *html.Node, tag string) []*html.Node {
	var result []*html.Node
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == tag {
			result = append(result, current)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	if node != nil {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	return result
}

func directChildren(node *html.Node, tag string) []*html.Node {
	var result []*html.Node
	if node == nil {
		return result
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == tag {
			result = append(result, child)
		}
	}
	return result
}

func attr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}
	return ""
}

func normalizedHTMLText(node *html.Node) string {
	if node == nil {
		return ""
	}
	var builder strings.Builder
	var visit func(*html.Node)
	visit = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}
