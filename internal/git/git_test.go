package git

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestFetchCommand(t *testing.T) {
	tests := []struct {
		name    string
		allRefs bool
		tags    bool
		want    []string
	}{
		{
			name: "upstream only without tags",
			want: []string{"fetch", "--quiet", "--no-tags", "origin", "main"},
		},
		{
			name: "upstream only with tags",
			tags: true,
			want: []string{"fetch", "--quiet", "origin", "main"},
		},
		{
			name:    "all refs without tags",
			allRefs: true,
			want:    []string{"fetch", "--quiet", "--no-tags", "origin"},
		},
		{
			name:    "all refs with tags",
			allRefs: true,
			tags:    true,
			want:    []string{"fetch", "--quiet", "origin"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := FetchCommand("origin", "main", test.allRefs, test.tags)
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("FetchCommand() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestHasNoUpstream(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{"fatal: no upstream configured for branch 'main'", true},
		{"fatal: HEAD does not point to a branch", true},
		{"fatal: not a git repository", false},
		{"", false},
	}
	for _, test := range tests {
		if got := HasNoUpstream(test.output); got != test.want {
			t.Errorf("HasNoUpstream(%q) = %v, want %v", test.output, got, test.want)
		}
	}
}

func TestIsTimeout(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{context.DeadlineExceeded, true},
		{context.Canceled, true},
		{errors.New("other error"), false},
		{nil, false},
	} {
		if got := IsTimeout(test.err); got != test.want {
			t.Errorf("IsTimeout(%v) = %v, want %v", test.err, got, test.want)
		}
	}
}

func TestRunReturnsContextCancellation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available: " + err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, t.TempDir(), "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestRunIncludesGitCommandInError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available: " + err.Error())
	}
	_, err := Run(context.Background(), t.TempDir(), "status")
	if err == nil || !strings.Contains(err.Error(), "git status") {
		t.Fatalf("Run() error = %v, want error containing git status", err)
	}
}
