package contentgithub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var errIncompleteSearchResults = errors.New("github search response is incomplete")

type searchItem struct {
	ID          int64    `json:"id"`
	FullName    string   `json:"full_name"`
	HTMLURL     string   `json:"html_url"`
	Description string   `json:"description"`
	Topics      []string `json:"topics"`
	Fork        bool     `json:"fork"`
}

type searchResponse struct {
	Items             []searchItem `json:"items"`
	IncompleteResults *bool        `json:"incomplete_results"`
}

func (response *searchResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Items             json.RawMessage `json:"items"`
		IncompleteResults json.RawMessage `json:"incomplete_results"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.Items) == 0 || bytes.Equal(bytes.TrimSpace(wire.Items), []byte("null")) {
		return errors.New("github search response has no items array")
	}
	var items []searchItem
	if err := json.Unmarshal(wire.Items, &items); err != nil {
		return err
	}
	var incompleteResults *bool
	if len(wire.IncompleteResults) > 0 {
		if bytes.Equal(bytes.TrimSpace(wire.IncompleteResults), []byte("null")) {
			return errors.New("github search response has invalid incomplete_results")
		}
		var incomplete bool
		if err := json.Unmarshal(wire.IncompleteResults, &incomplete); err != nil {
			return err
		}
		incompleteResults = &incomplete
	}
	response.Items = items
	response.IncompleteResults = incompleteResults
	return nil
}

func (item searchItem) candidate() repositoryCandidate {
	return repositoryCandidate{
		ID: item.ID, FullName: item.FullName, HTMLURL: item.HTMLURL,
		Description: item.Description, Topics: append([]string(nil), item.Topics...), Fork: item.Fork,
	}
}

func (c *Collector) discover(ctx context.Context, now time.Time) ([]repositoryCandidate, *searchState, error) {
	s := c.search
	if s == nil {
		s = &searchState{
			cycle:       c.discoveryCycle + 1,
			rowsByQuery: make([][]repositoryCandidate, len(c.cfg.Queries)),
		}
	}
	for s.query < len(c.cfg.Queries) {
		if s.page == 0 {
			s.querySeen = make(map[int64]bool)
			s.sortBy = "stars"
			if (s.cycle+s.query)%2 == 1 {
				s.sortBy = "updated"
			}
		}

		page := s.page + 1
		query := url.Values{
			"q":        {addSearchQualifiers(c.cfg.Queries[s.query])},
			"page":     {strconv.Itoa(page)},
			"per_page": {strconv.Itoa(c.cfg.PageSize)},
			"sort":     {s.sortBy},
			"order":    {"desc"},
		}
		var result searchResponse
		if err := c.client.getJSON(ctx, now, "search", "/search/repositories", query, &result); err != nil {
			return nil, s, err
		}
		if result.IncompleteResults != nil && *result.IncompleteResults {
			return nil, s, errIncompleteSearchResults
		}
		if len(result.Items) > c.cfg.PageSize {
			return nil, s, errors.New("github search response exceeds requested page size")
		}
		for _, row := range result.Items {
			if row.ID <= 0 || row.Fork || row.FullName == "" || s.querySeen[row.ID] {
				continue
			}
			s.querySeen[row.ID] = true
			s.rowsByQuery[s.query] = append(s.rowsByQuery[s.query], row.candidate())
		}
		if page < c.cfg.MaxPagesPerQuery && len(result.Items) == c.cfg.PageSize {
			s.page = page
		} else {
			s.query++
			s.page = 0
		}
	}

	c.discoveryCycle = s.cycle
	return mergeCandidateGroups(s.rowsByQuery, c.cfg.MaxCandidates), nil, nil
}

func addSearchQualifiers(query string) string {
	const qualifiers = "is:public archived:false fork:false"
	fields := strings.Fields(query)
	seen := make(map[string]bool, len(fields)+3)
	for _, field := range fields {
		seen[field] = true
	}
	for _, qualifier := range strings.Fields(qualifiers) {
		if !seen[qualifier] {
			fields = append(fields, qualifier)
		}
	}
	return strings.Join(fields, " ")
}

func mergeCandidateGroups(groups [][]repositoryCandidate, limit int) []repositoryCandidate {
	if limit <= 0 {
		return nil
	}
	out := make([]repositoryCandidate, 0, limit)
	seen := make(map[int64]bool, limit)
	for rank := 0; len(out) < limit; rank++ {
		exhausted := true
		for _, group := range groups {
			if rank >= len(group) {
				continue
			}
			exhausted = false
			candidate := group[rank]
			if candidate.ID <= 0 || seen[candidate.ID] {
				continue
			}
			seen[candidate.ID] = true
			out = append(out, candidate)
			if len(out) == limit {
				break
			}
		}
		if exhausted {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
