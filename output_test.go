package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

var errFake = errors.New("boom")

// unsetEnv removes a variable for the rest of the test and restores it after.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	original, existed := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			os.Setenv(key, original)
		} else {
			os.Unsetenv(key)
		}
	})
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"alpha", 8, "alpha"},
		{"alpha", 5, "alpha"},
		{"alphavalue", 5, "alph…"},
		{"alphavalue", 1, "…"},
		{"héllo", 3, "hé…"},
		{"anything", 0, "anything"},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.max); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSanitizeStripsControlCharacters(t *testing.T) {
	got := sanitize("evil\x1b[31mred\ntab\there\x7f")
	want := "evil?[31mred\\ntab\\there?"
	if got != want {
		t.Errorf("sanitize() = %q, want %q", got, want)
	}
}

func TestRetryCommandQuotesNames(t *testing.T) {
	results := []updateResult{
		{repository: repository{name: "alpha"}},
		{repository: repository{name: "my repo"}},
		{repository: repository{name: "it's"}},
	}
	got := retryCommand(results, "/home/user/projects")
	want := "git-sync --only alpha --only 'my repo' --only 'it'\\''s' /home/user/projects"
	if got != want {
		t.Errorf("retryCommand =\n  %q\nwant\n  %q", got, want)
	}

	// The default directory is omitted so the command can be pasted anywhere.
	if got := retryCommand(results, "."); strings.Contains(got, " .") {
		t.Errorf("retryCommand with \".\" should not append the root: %q", got)
	}
}

func TestPluralization(t *testing.T) {
	if got := plural(1, "repository", "repositories"); got != "repository" {
		t.Errorf("plural(1) = %q", got)
	}
	if got := plural(2, "repository", "repositories"); got != "repositories" {
		t.Errorf("plural(2) = %q", got)
	}
	if got := pluralCommits(1); got != "1 commit" {
		t.Errorf("pluralCommits(1) = %q", got)
	}
	if got := pluralCommits(3); got != "3 commits" {
		t.Errorf("pluralCommits(3) = %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		1500 * time.Millisecond: "1.5s",
		250 * time.Millisecond:  "250ms",
		1500 * time.Microsecond: "2ms",
	}
	for in, want := range cases {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestColorEnabled(t *testing.T) {
	var buf bytes.Buffer

	if !colorEnabled(colorAlways, &buf) {
		t.Error("colorAlways should enable color even for a non-terminal writer")
	}
	t.Setenv("TERM", "xterm-256color")
	if colorEnabled(colorAlways, &buf) != true {
		t.Error("colorAlways should win over environment")
	}
	if colorEnabled(colorNever, &buf) {
		t.Error("colorNever should disable color")
	}
	t.Setenv("NO_COLOR", "1")
	if colorEnabled(colorAuto, &buf) {
		t.Error("NO_COLOR should disable color")
	}
	unsetEnv(t, "NO_COLOR")
	t.Setenv("TERM", "dumb")
	if colorEnabled(colorAuto, &buf) {
		t.Error("TERM=dumb should disable color")
	}
	unsetEnv(t, "TERM")
	if colorEnabled(colorAuto, &buf) {
		t.Error("an empty TERM should disable color")
	}
	t.Setenv("TERM", "xterm-256color")
	// A bytes.Buffer is not a terminal, so auto mode must stay off.
	if colorEnabled(colorAuto, &buf) {
		t.Error("auto mode should disable color for a non-terminal writer")
	}
}

func TestParseColorMode(t *testing.T) {
	cases := map[string]colorMode{
		"auto":   colorAuto,
		"":       colorAuto,
		"weird":  colorAuto,
		"always": colorAlways,
		"ALWAYS": colorAlways,
		"never":  colorNever,
		"Never":  colorNever,
	}
	for in, want := range cases {
		if got := parseColorMode(in); got != want {
			t.Errorf("parseColorMode(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStatusLabelsAndGlyphs(t *testing.T) {
	if got := statusLabel(statusUpToDate); got != "up-to-date" {
		t.Errorf("statusLabel(up-to-date) = %q", got)
	}
	if got := statusLabel(statusUpdated); got != "fast-forwarded" {
		t.Errorf("statusLabel(fast-forwarded) = %q", got)
	}
	// Glyphs must differ per status so the report stays scannable without color.
	seen := map[string]status{}
	for _, s := range []status{statusUpToDate, statusUpdated, statusSkipped, statusFailed} {
		glyph := statusGlyph(palette{}, s)
		if other, dup := seen[glyph]; dup {
			t.Errorf("statuses %v and %v share the glyph %q", other, s, glyph)
		}
		seen[glyph] = s
	}
	// Disabled palette must emit no escape codes.
	if strings.Contains(statusGlyph(palette{}, statusFailed), "\033") {
		t.Error("disabled palette should not emit ANSI escapes")
	}
}

func TestWriteJSONMapsStatuses(t *testing.T) {
	var buf bytes.Buffer
	results := []updateResult{
		{repository: repository{name: "alpha"}, status: statusUpdated, commits: 2, remote: "origin", branch: "main"},
		{repository: repository{name: "beta"}, status: statusUpToDate, duration: 900 * time.Millisecond},
		{repository: repository{name: "gamma"}, status: statusFailed, err: errFake},
	}
	if err := writeJSON(&buf, "/projects", results, 3*time.Second, false); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{
		`"root": "/projects"`,
		`"name": "alpha"`,
		`"status": "fast-forwarded"`,
		`"commits": 2`,
		`"name": "beta"`,
		`"durationMs": 900`,
		`"status": "failed"`,
		`"total": 3`,
		`"fastForwarded": 1`,
		`"failed": 1`,
		`"durationMs": 3000`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON output missing %s\n%s", want, out)
		}
	}
}

func TestNonEmptyLines(t *testing.T) {
	got := nonEmptyLines("a\n\n\nb\r\n\n")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("nonEmptyLines = %#v, want [a b]", got)
	}
	if got := nonEmptyLines(""); len(got) != 0 {
		t.Errorf("nonEmptyLines(empty) = %#v, want none", got)
	}
}
