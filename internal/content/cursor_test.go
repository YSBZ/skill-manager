package content

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCursorBindsQueryAndVersion(t *testing.T) {
	q := Query{Kind: "news", View: "all", Window: "7d", Limit: 20}
	token, err := encodeCursor(q, 4, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := decodeCursor(token, q, 4); err != nil || got != 20 {
		t.Fatalf("decode = %d, %v", got, err)
	}
	if _, err := decodeCursor(token, q, 5); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("version error = %v", err)
	}
	q.View = "selected"
	if _, err := decodeCursor(token, q, 4); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("query error = %v", err)
	}
}

func TestNormalizeQueryDefaultLimitAndUpperBound(t *testing.T) {
	for _, limit := range []int{0, 20} {
		q, err := normalizeQuery(Query{Kind: "news", Limit: limit})
		if err != nil || q.Limit != 20 {
			t.Fatalf("limit %d -> %#v, %v", limit, q, err)
		}
	}
	if _, err := normalizeQuery(Query{Kind: "news", Limit: 51}); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("limit 51 error = %v", err)
	}
}

func TestNormalizeQueryKindsAndRuneLimit(t *testing.T) {
	for _, tc := range []struct {
		query Query
		want  Query
	}{
		{Query{}, Query{Kind: "news", View: "selected", Window: "7d", Limit: 20}},
		{Query{Kind: "project"}, Query{Kind: "project", View: "weekly", Limit: 20}},
		{Query{Kind: "topic", Window: "24h"}, Query{Kind: "topic", Limit: 20}},
	} {
		got, err := normalizeQuery(tc.query)
		if err != nil || got != tc.want {
			t.Errorf("normalize %#v = %#v, %v; want %#v", tc.query, got, err, tc.want)
		}
	}
	if _, err := normalizeQuery(Query{Kind: "news", Q: strings.Repeat("界", 201)}); !errors.Is(err, ErrBadQuery) {
		t.Fatalf("201 rune query error = %v", err)
	}
	for _, q := range []Query{
		{Kind: "news", View: "weekly"},
		{Kind: "news", Window: "30d"},
		{Kind: "project", View: "all"},
		{Kind: "topic", View: "all"},
		{Kind: "unknown"},
		{Kind: "news", Limit: -1},
	} {
		if _, err := normalizeQuery(q); !errors.Is(err, ErrBadQuery) {
			t.Errorf("normalize invalid %#v error = %v", q, err)
		}
	}
}

func TestCursorRejectsBadBase64AndNegativeOffsets(t *testing.T) {
	q := Query{Kind: "news", View: "selected", Window: "7d", Limit: 20}
	if _, err := decodeCursor("%%%", q, 1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("bad base64 error = %v", err)
	}
	if _, err := encodeCursor(q, 1, -1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("negative encode offset error = %v", err)
	}
	payload := cursorPayload{Version: 1, Offset: -1}
	payload.Query, _ = queryHash(q)
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCursor(base64.RawURLEncoding.EncodeToString(b), q, 1); !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("negative decoded offset error = %v", err)
	}
}
