package content

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type cursorPayload struct {
	Query   string `json:"q"`
	Version int64  `json:"v"`
	Offset  int    `json:"o"`
}

func queryHash(q Query) (string, error) {
	q.Cursor = ""
	b, err := json.Marshal(q)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func encodeCursor(q Query, version int64, offset int) (string, error) {
	if offset < 0 || version < 0 {
		return "", ErrCursorExpired
	}
	h, err := queryHash(q)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(cursorPayload{Query: h, Version: version, Offset: offset})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(token string, q Query, version int64) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, ErrCursorExpired
	}
	var payload cursorPayload
	if err := json.Unmarshal(b, &payload); err != nil || payload.Offset < 0 || payload.Version != version {
		return 0, ErrCursorExpired
	}
	h, err := queryHash(q)
	if err != nil || payload.Query != h {
		return 0, ErrCursorExpired
	}
	return payload.Offset, nil
}

func normalizeQuery(q Query) (Query, error) {
	q.Q = strings.TrimSpace(q.Q)
	if q.Kind == "" {
		q.Kind = "news"
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || utf8.RuneCountInString(q.Q) > 200 {
		return Query{}, ErrBadQuery
	}

	switch q.Kind {
	case "news":
		if q.View == "" {
			q.View = "selected"
		}
		if q.Window == "" {
			q.Window = "7d"
		}
		if (q.View != "selected" && q.View != "all") || (q.Window != "24h" && q.Window != "7d") {
			return Query{}, ErrBadQuery
		}
	case "project":
		if q.View == "" {
			q.View = "weekly"
		}
		if q.View != "weekly" && q.View != "new" && q.View != "scenarios" {
			return Query{}, ErrBadQuery
		}
		q.Window = ""
	case "topic", "daily", "weekly", "monthly":
		if q.View != "" {
			return Query{}, ErrBadQuery
		}
		q.Window = ""
	default:
		return Query{}, ErrBadQuery
	}
	return q, nil
}
