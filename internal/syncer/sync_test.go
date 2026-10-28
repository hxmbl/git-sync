package syncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/git-sync/internal/git"
	"github.com/hxmbl/git-sync/internal/model"
)

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=syncer-test",
		"GIT_AUTHOR_EMAIL=syncer-test@example.com",
		"GIT_COMMITTER_NAME=syncer-test",
		"GIT_COMMITTER_EMAIL=syncer-test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func newSyncFixture(t *testing.T) (working, seed string, repo model.Repository) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available: " + err.Error())
	}

	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	runGitTest(t, root, "init", "--quiet", "--bare", "-b", "main", remote)
	seed = filepath.Join(root, "seed")
	runGitTest(t, root, "clone", "--quiet", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "file.txt"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, seed, "add", "file.txt")
	runGitTest(t, seed, "commit", "--quiet", "-m", "initial")
	runGitTest(t, seed, "push", "--quiet", "-u", "origin", "HEAD:refs/heads/main")

	working = filepath.Join(root, "working")
	runGitTest(t, root, "clone", "--quiet", remote, working)
	gitDir, err := git.ResolveGitDir(working)
	if err != nil {
		t.Fatal(err)
	}
	return working, seed, model.Repository{
		Name:   "working",
		Path:   working,
		GitDir: gitDir,
	}
}

func commitTestFile(t *testing.T, dir, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, dir, "add", "file.txt")
	runGitTest(t, dir, "commit", "--quiet", "-m", message)
}

func TestFindRepositoriesAndFilter(t *testing.T) {
	root := t.TempDir()
	alpha := filepath.Join(root, "alpha")
	beta := filepath.Join(root, "beta")
	notes := filepath.Join(root, "notes")
	for _, path := range []string{alpha, beta, notes} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(alpha, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, ".git"), []byte("gitdir: ../external/beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("not a repo"), 0o644); err != nil {
		t.Fatal(err)
	}

	repositories, err := FindRepositories(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 {
		t.Fatalf("FindRepositories returned %d repositories, want 2: %#v", len(repositories), repositories)
	}
	if repositories[0].Name != "alpha" || repositories[0].GitDir != filepath.Join(alpha, ".git") {
		t.Errorf("first repository = %#v, want alpha with its .git directory", repositories[0])
	}
	if repositories[1].Name != "beta" || repositories[1].GitDir != filepath.Join(root, "external", "beta") {
		t.Errorf("second repository = %#v, want beta with resolved worktree gitdir", repositories[1])
	}

	filtered := FilterRepositories(repositories, []string{"alpha", "beta"}, []string{"beta"})
	if len(filtered) != 1 || filtered[0].Name != "alpha" {
		t.Errorf("FilterRepositories(only alpha,beta; skip beta) = %#v, want alpha", filtered)
	}
	if got := FilterRepositories(repositories, nil, nil); len(got) != len(repositories) {
		t.Errorf("empty filters returned %d repositories, want %d", len(got), len(repositories))
	}
	if _, err := FindRepositories(filepath.Join(root, "missing")); err == nil {
		t.Error("FindRepositories should return an error for a missing root")
	}
}

func TestSyncRepositoryStatuses(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(t *testing.T, working, seed string)
		options    model.SyncOptions
		wantStatus model.Status
		wantDetail string
		wantCommit int
	}{
		{
			name:       "up to date",
			wantStatus: model.StatusUpToDate,
			wantDetail: "up to date",
		},
		{
			name: "fast forward",
			prepare: func(t *testing.T, _, seed string) {
				commitTestFile(t, seed, "remote update", "remote update")
				runGitTest(t, seed, "push", "--quiet")
			},
			wantStatus: model.StatusUpdated,
			wantDetail: "fast-forwarded 1 commit",
			wantCommit: 1,
		},
		{
			name: "dry run",
			prepare: func(t *testing.T, _, seed string) {
				commitTestFile(t, seed, "remote update", "remote update")
				runGitTest(t, seed, "push", "--quiet")
			},
			options:    model.SyncOptions{DryRun: true, Timeout: time.Minute},
			wantStatus: model.StatusUpdated,
			wantDetail: "would fast-forward 1 commit",
			wantCommit: 1,
		},
		{
			name: "ahead",
			prepare: func(t *testing.T, working, _ string) {
				commitTestFile(t, working, "local update", "local update")
			},
			wantStatus: model.StatusAhead,
			wantDetail: "needs push (1 commit ahead)",
			wantCommit: 1,
		},
		{
			name: "diverged",
			prepare: func(t *testing.T, working, seed string) {
				commitTestFile(t, seed, "remote update", "remote update")
				runGitTest(t, seed, "push", "--quiet")
				commitTestFile(t, working, "local update", "local update")
			},
			wantStatus: model.StatusFailed,
			wantDetail: "has diverged from upstream",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			working, seed, repo := newSyncFixture(t)
			if test.prepare != nil {
				test.prepare(t, working, seed)
			}
			options := test.options
			if options.Timeout == 0 {
				options.Timeout = time.Minute
			}
			result := SyncRepository(context.Background(), repo, options)
			if result.Status != test.wantStatus || result.Detail != test.wantDetail {
				t.Fatalf("SyncRepository() = status %v, detail %q; want status %v, detail %q",
					result.Status, result.Detail, test.wantStatus, test.wantDetail)
			}
			if result.Commits != test.wantCommit {
				t.Errorf("SyncRepository() commits = %d, want %d", result.Commits, test.wantCommit)
			}
			if result.Repository.Name != repo.Name || result.Duration <= 0 {
				t.Errorf("result omitted repository or duration: %#v", result)
			}
			if test.name == "fast forward" {
				head := runGitTest(t, working, "rev-parse", "HEAD")
				upstream := runGitTest(t, working, "rev-parse", "origin/main")
				if head != upstream {
					t.Errorf("HEAD %s != upstream %s after fast-forward", head, upstream)
				}
			}
			if test.name == "dry run" {
				head := runGitTest(t, working, "rev-parse", "HEAD")
				upstream := runGitTest(t, working, "rev-parse", "origin/main")
				if head == upstream {
					t.Error("dry run unexpectedly updated HEAD")
				}
			}
		})
	}
}

func TestSyncRepositoryReportsMissingUpstreamAndBrokenRepository(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(t *testing.T, root string) model.Repository
		wantStatus model.Status
		wantDetail string
	}{
		{
			name: "no upstream",
			prepare: func(t *testing.T, root string) model.Repository {
				path := filepath.Join(root, "local")
				runGitTest(t, root, "init", "--quiet", "-b", "main", path)
				commitTestFile(t, path, "initial", "initial")
				return model.Repository{Name: "local", Path: path, GitDir: filepath.Join(path, ".git")}
			},
			wantStatus: model.StatusSkipped,
			wantDetail: "has no upstream",
		},
		{
			name: "broken repository",
			prepare: func(t *testing.T, root string) model.Repository {
				path := filepath.Join(root, "broken")
				if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
				return model.Repository{Name: "broken", Path: path, GitDir: filepath.Join(path, ".git")}
			},
			wantStatus: model.StatusFailed,
			wantDetail: "could not be read",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := exec.LookPath("git"); err != nil {
				t.Skip("git is not available: " + err.Error())
			}
			repo := test.prepare(t, t.TempDir())
			result := SyncRepository(context.Background(), repo, model.SyncOptions{Timeout: time.Minute})
			if result.Status != test.wantStatus || result.Detail != test.wantDetail {
				t.Fatalf("SyncRepository() = status %v, detail %q; want status %v, detail %q",
					result.Status, result.Detail, test.wantStatus, test.wantDetail)
			}
			if test.wantStatus == model.StatusFailed && result.Err == nil {
				t.Error("failed result should include an error")
			}
		})
	}
}

func TestSyncRepositoryTimeout(t *testing.T) {
	_, _, repo := newSyncFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := SyncRepository(ctx, repo, model.SyncOptions{Timeout: time.Minute})
	if result.Status != model.StatusFailed || result.Detail != "timed out while reading" {
		t.Fatalf("SyncRepository(cancelled) = status %v, detail %q, want timeout failure",
			result.Status, result.Detail)
	}
	if result.Err == nil {
		t.Fatal("cancelled sync should report its error")
	}
}

func TestFilterRepositoriesUsesExactNames(t *testing.T) {
	repositories := []model.Repository{{Name: "api"}, {Name: "api-v2"}}
	got := FilterRepositories(repositories, []string{"api"}, nil)
	if len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("FilterRepositories exact match = %#v, want only api", got)
	}
}
