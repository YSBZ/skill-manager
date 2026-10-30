package cli

import (
	"reflect"
	"testing"
)

// TestIsInvocation guards the CLI-vs-daemon dispatch gate. The bug it locks in:
// an unknown bare word must route to the CLI (which reports "未知命令" + usage)
// rather than silently boot the daemon window with exit 0. Dash-prefixed launch
// args (Finder's -psn_…, the daemon's -central/-no-open) must NOT trip the CLI.
func TestIsInvocation(t *testing.T) {
	cli := []string{
		"list", "enable", "disable", "move", "adopt", "sync", // known subcommands
		"help",         // bare help word
		"frobnicate",   // unknown word → still routes → Run prints usage error
		"-h", "--help", // help flags route to CLI usage
	}
	for _, a := range cli {
		if !IsInvocation(a) {
			t.Errorf("IsInvocation(%q) = false, want true (should route to CLI)", a)
		}
	}
	daemon := []string{
		"-central", "--central", "-no-open", "--no-open", // daemon flags
		"-psn_0_12345", // macOS Finder launch arg
	}
	for _, a := range daemon {
		if IsInvocation(a) {
			t.Errorf("IsInvocation(%q) = true, want false (should stay on daemon path)", a)
		}
	}
}

// TestParse covers the positional/flag splitter: --k v, --k=v, and a trailing
// --k (before another flag or end) as a boolean "true".
func TestParse(t *testing.T) {
	pos, fl := parse([]string{"@agents/foo", "--target", "/tmp/x", "--mode=follow", "--count"})
	if want := []string{"@agents/foo"}; !reflect.DeepEqual(pos, want) {
		t.Errorf("positionals = %v, want %v", pos, want)
	}
	if fl["target"] != "/tmp/x" {
		t.Errorf("--target = %q, want /tmp/x", fl["target"])
	}
	if fl["mode"] != "follow" {
		t.Errorf("--mode = %q, want follow (=form)", fl["mode"])
	}
	if fl["count"] != "true" {
		t.Errorf("--count = %q, want true (bare flag)", fl["count"])
	}
}

// TestOneLine collapses newlines/whitespace and truncates, so `list <repo>`
// prints exactly one greppable/countable line per skill.
func TestOneLine(t *testing.T) {
	if got := oneLine("a\n\nb   c\td"); got != "a b c d" {
		t.Errorf("oneLine collapse = %q, want %q", got, "a b c d")
	}
	long := make([]byte, 300)
	for i := range long {
		long[i] = 'x'
	}
	if got := oneLine(string(long)); len([]rune(got)) != 100 {
		t.Errorf("oneLine truncation len = %d runes, want 100 (99 + ellipsis)", len([]rune(got)))
	}
}
