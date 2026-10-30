package content

import (
	"strings"
	"testing"
)

func TestValidateID(t *testing.T) {
	for _, tc := range []struct {
		id      string
		wantErr bool
	}{
		{"news_1", false},
		{"topic_a-1", false},
		{"", true},
		{"../secret", true},
		{strings.Repeat("x", 161), true},
	} {
		if err := ValidateID(tc.id); (err != nil) != tc.wantErr {
			t.Errorf("ValidateID(%q) error = %v, wantErr %v", tc.id, err, tc.wantErr)
		}
	}
}
