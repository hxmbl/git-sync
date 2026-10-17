package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available: " + err.Error())
	}
}

func testGitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=git-sync-test",
		"GIT_AUTHOR_EMAIL=git-sync-test@example.com",
		"GIT_COMMITTER_NAME=git-sync-test",
		"GIT_COMMITTER_EMAIL=git-sync-test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func newBare(t *testing.T, parent, name string) string {
	t.Helper()
	remote := filepath.Join(parent, name+".git")
	gitRun(t, parent, "init", "--quiet", "--bare", "-b", "main", remote)
	return remote
}

func newClone(t *testing.T, remote, dest string) string {
	t.Helper()
	gitRun(t, filepath.Dir(dest), "clone", "--quiet", remote, dest)
	gitRun(t, dest, "symbolic-ref", "HEAD", "refs/heads/main")
	return dest
}

func commitFile(t *testing.T, dir, name, content, message string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	gitRun(t, dir, "add", "--all")
	gitRun(t, dir, "commit", "--quiet", "-m", message)
}

func pushToOrigin(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "push", "--quiet", "-u", "origin", "HEAD:refs/heads/main")
}

// seededRepo creates a bare remote, publishes `commits` commits from a seed
// clone, then clones a working copy into the projects directory.
func seededRepo(t *testing.T, remotes, seeds, projects, name string, commits int) (working, seed string) {
	t.Helper()
	remote := newBare(t, remotes, name)
	seed = newClone(t, remote, filepath.Join(seeds, name))
	for i := 1; i <= commits; i++ {
		commitFile(t, seed, "f.txt", fmt.Sprintf("%s-%d", name, i), fmt.Sprintf("c%d", i))
	}
	pushToOrigin(t, seed)
	working = newClone(t, remote, filepath.Join(projects, name))
	return working, seed
}

// resultLines returns everything printed before the summary block, so
// assertions cannot be satisfied by the failure recap alone.
func resultLines(output string) string {
	if i := strings.Index(output, "\n──────── Summary"); i >= 0 {
		return output[:i]
	}
	return output
}

func assertLine(t *testing.T, output, name, detail string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, name) && strings.Contains(line, detail) {
			return
		}
	}
	t.Errorf("no line containing both %q and %q in:\n%s", name, detail, output)
}

func setupProjects(t *testing.T) string {
	t.Helper()
	requireGit(t)

	projects := t.TempDir()
	remotes := t.TempDir()
	seeds := t.TempDir()

	// alpha: the remote moves two commits ahead of the working copy.
	_, alphaSeed := seededRepo(t, remotes, seeds, projects, "alpha", 1)
	commitFile(t, alphaSeed, "f.txt", "alpha-2", "c2")
	commitFile(t, alphaSeed, "f.txt", "alpha-3", "c3")
	pushToOrigin(t, alphaSeed)

	// current: the working copy already has everything.
	seededRepo(t, remotes, seeds, projects, "current", 1)

	// ahead: the working copy has an unpushed local commit.
	aheadWorking, _ := seededRepo(t, remotes, seeds, projects, "ahead", 1)
	commitFile(t, aheadWorking, "local.txt", "unpushed", "local only")

	// diverged: local and remote each gain a different commit.
	divergedWorking, divergedSeed := seededRepo(t, remotes, seeds, projects, "diverged", 1)
	commitFile(t, divergedSeed, "f.txt", "remote side", "remote")
	pushToOrigin(t, divergedSeed)
	commitFile(t, divergedWorking, "other.txt", "local side", "local")

	// dirty: the remote changes a file that has uncommitted local edits.
	dirtyWorking, dirtySeed := seededRepo(t, remotes, seeds, projects, "dirty", 1)
	commitFile(t, dirtySeed, "f.txt", "remote change", "remote")
	pushToOrigin(t, dirtySeed)
	writeFile(t, filepath.Join(dirtyWorking, "f.txt"), "uncommitted local edit")

	// local: a repository with no upstream at all.
	local := filepath.Join(projects, "local")
	gitRun(t, projects, "init", "--quiet", "-b", "main", local)
	commitFile(t, local, "f.txt", "local", "c1")

	// not-a-repo: a plain directory that must be ignored.
	if err := os.MkdirAll(filepath.Join(projects, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}

	return projects
}

func TestSyncEndToEnd(t *testing.T) {
	projects := setupProjects(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--color", "never", "-j", "4", projects}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("first run exit = %d, want 1 (two repositories need attention)\nstdout:\n%s\nstderr:\n%s",
			code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("first run wrote to stderr: %s", stderr.String())
	}

	results := resultLines(stdout.String())
	assertLine(t, results, "alpha", "fast-forwarded 2 commits")
	assertLine(t, results, "current", "up to date")
	assertLine(t, results, "ahead", "up to date (local ahead by 1 commit)")
	assertLine(t, results, "diverged", "update failed (diverged)")
	assertLine(t, results, "dirty", "update failed")
	assertLine(t, results, "local", "skipped (no upstream tracking branch)")

	if !strings.Contains(stdout.String(), "2 failed") {
		t.Errorf("summary should report 2 failed:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Retry the failed repositories:") {
		t.Errorf("summary should suggest a retry command:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--only dirty") {
		t.Errorf("retry command should list the failed repositories:\n%s", stdout.String())
	}

	// The fast-forwarded repository must now be level with its upstream.
	head := gitRun(t, filepath.Join(projects, "alpha"), "rev-parse", "HEAD")
	upstream := gitRun(t, filepath.Join(projects, "alpha"), "rev-parse", "origin/main")
	if head != upstream {
		t.Errorf("alpha HEAD = %s, origin/main = %s; want equal after sync", head, upstream)
	}

	// A second run must re-report the repositories that still need attention.
	// Previously a diverged repository was misreported as up to date on the
	// second run because only the remote-tracking ref was compared.
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"--color", "never", "-j", "4", projects}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("second run exit = %d, want 1\nstdout:\n%s", code, stdout.String())
	}

	second := resultLines(stdout.String())
	assertLine(t, second, "alpha", "up to date")
	assertLine(t, second, "current", "up to date")
	assertLine(t, second, "ahead", "up to date (local ahead by 1 commit)")
	assertLine(t, second, "diverged", "update failed (diverged)")
	assertLine(t, second, "dirty", "update failed")
	assertLine(t, second, "local", "skipped (no upstream tracking branch)")
	if strings.Contains(stdout.String(), "fast-forwarded") {
		t.Errorf("second run should not fast-forward anything:\n%s", stdout.String())
	}
}

func TestDryRunDoesNotModifyRepositories(t *testing.T) {
	requireGit(t)

	projects := t.TempDir()
	remotes := t.TempDir()
	seeds := t.TempDir()
	working, seed := seededRepo(t, remotes, seeds, projects, "alpha", 1)
	commitFile(t, seed, "f.txt", "remote moved", "c2")
	pushToOrigin(t, seed)

	before := gitRun(t, working, "rev-parse", "HEAD")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--color", "never", "--dry-run", projects}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("dry-run exit = %d, want 0\n%s", code, stdout.String())
	}
	assertLine(t, resultLines(stdout.String()), "alpha", "would fast-forward 1 commit")

	after := gitRun(t, working, "rev-parse", "HEAD")
	if before != after {
		t.Errorf("dry-run changed HEAD: %s -> %s", before, after)
	}
}

func TestFetchesOnlyUpstreamBranchByDefault(t *testing.T) {
	requireGit(t)

	projects := t.TempDir()
	remotes := t.TempDir()
	seeds := t.TempDir()

	remote := newBare(t, remotes, "branches")
	seed := newClone(t, remote, filepath.Join(seeds, "branches"))
	commitFile(t, seed, "f.txt", "main work", "c1")
	pushToOrigin(t, seed)
	gitRun(t, seed, "checkout", "--quiet", "-b", "feature")
	commitFile(t, seed, "g.txt", "feature work", "c2")
	gitRun(t, seed, "push", "--quiet", "origin", "feature")
	gitRun(t, seed, "checkout", "--quiet", "main")

	working := newClone(t, remote, filepath.Join(projects, "branches"))

	// Move the feature branch on the remote after the clone.
	seedCheckout := filepath.Join(seeds, "branches-feature")
	gitRun(t, filepath.Dir(seedCheckout), "clone", "--quiet", remote, seedCheckout)
	gitRun(t, seedCheckout, "checkout", "--quiet", "feature")
	commitFile(t, seedCheckout, "g.txt", "more feature work", "c3")
	gitRun(t, seedCheckout, "push", "--quiet", "origin", "HEAD:refs/heads/feature")

	staleBefore := gitRun(t, working, "rev-parse", "origin/feature")

	var stdout bytes.Buffer
	if code := run([]string{"--color", "never", projects}, &stdout, io.Discard); code != 0 {
		t.Fatalf("default run exit = %d\n%s", code, stdout.String())
	}
	staleAfterDefault := gitRun(t, working, "rev-parse", "origin/feature")
	if staleBefore != staleAfterDefault {
		t.Errorf("default run refreshed origin/feature (%s -> %s); only the upstream should be fetched",
			staleBefore, staleAfterDefault)
	}

	stdout.Reset()
	if code := run([]string{"--color", "never", "--all-refs", projects}, &stdout, io.Discard); code != 0 {
		t.Fatalf("--all-refs run exit = %d\n%s", code, stdout.String())
	}
	fresh := gitRun(t, working, "rev-parse", "origin/feature")
	if fresh == staleBefore {
		t.Errorf("--all-refs did not refresh origin/feature (still %s)", fresh)
	}
}

func TestBrokenRepositoryIsReportedAsFailure(t *testing.T) {
	requireGit(t)
	projects := t.TempDir()

	// A directory that looks like a repository but cannot be read at all. This
	// must not be mistaken for a repository that simply has no upstream.
	if err := os.MkdirAll(filepath.Join(projects, "broken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--color", "never", projects}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 (a broken repository needs attention)\n%s", code, stdout.String())
	}
	assertLine(t, resultLines(stdout.String()), "broken", "could not read repository")
	if strings.Contains(stdout.String(), "skipped") {
		t.Errorf("a broken repository must not be reported as skipped:\n%s", stdout.String())
	}
}

func TestFlagAndDirectoryOrdering(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		args []string
	}{
		{"directory first", []string{dir, "-j", "2", "--color", "never"}},
		{"flags first", []string{"--color", "never", "-j", "2", dir}},
		{"directory between flags", []string{"-j", "2", dir, "--color", "never"}},
		{"after a double dash", []string{"--color", "never", "--", dir}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(c.args, &stdout, &stderr); code != 0 {
				t.Errorf("exit = %d, want 0\nstderr: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), filepath.Base(dir)) {
				t.Errorf("the projects directory should have been read:\n%s", stdout.String())
			}
		})
	}

	// A value flag left dangling is an error, not a silent attempt to consume
	// whatever happens to follow it.
	var stdout, stderr bytes.Buffer
	if code := run([]string{dir, "--only"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag needs an argument") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		want   int
		stderr string
	}{
		{"unknown flag", []string{"--bogus"}, 2, "flag provided but not defined"},
		{"zero jobs", []string{"-j", "0"}, 2, "-j must be at least 1"},
		{"negative timeout", []string{"--timeout", "-1s"}, 2, "--timeout must be positive"},
		{"quiet and verbose", []string{"-q", "-v"}, 2, "cannot be combined"},
		{"too many directories", []string{"one", "two"}, 2, "at most one projects directory"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(c.args, &stdout, &stderr); code != c.want {
				t.Errorf("exit = %d, want %d", code, c.want)
			}
			if !strings.Contains(stderr.String(), c.stderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), c.stderr)
			}
			// The full usage belongs to --help, not to an error message.
			if strings.Contains(stderr.String(), "Usage:") {
				t.Errorf("errors should not dump the usage text:\n%s", stderr.String())
			}
			if !strings.Contains(stderr.String(), "Run 'git-sync --help'") {
				t.Errorf("errors should point at --help:\n%s", stderr.String())
			}
		})
	}
}

func TestHelpAndVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Errorf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("--help should print usage to stdout:\n%s", stdout.String())
	}
	for _, want := range []string{"Essential:", "Selection:", "Fetching:", "Output:", "-j", "--jobs", "--timeout", "--json", "--dry-run"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("--help missing %q", want)
		}
	}
	if stderr.Len() != 0 {
		t.Errorf("--help wrote to stderr: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-V"}, &stdout, &stderr); code != 0 {
		t.Errorf("-V exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "git-sync ") {
		t.Errorf("-V output = %q", stdout.String())
	}
}

func TestMissingProjectsDirectory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{filepath.Join(t.TempDir(), "does-not-exist")}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "could not read projects directory") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestEmptyProjectsDirectory(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--color", "never", dir}, &stdout, &stderr); code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "No Git repositories found") {
		t.Errorf("stdout = %q", stdout.String())
	}
}
