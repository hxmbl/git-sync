package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testOID = "0123456789abcdef0123456789abcdef01234567"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveGitDir(t *testing.T) {
	root := t.TempDir()

	// Not a repository at all.
	if gitDir, err := resolveGitDir(root); err != nil || gitDir != "" {
		t.Fatalf("resolveGitDir(non-repo) = %q, %v; want \"\", nil", gitDir, err)
	}

	// Plain ".git" directory.
	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(filepath.Join(plain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveGitDir(plain); err != nil || got != filepath.Join(plain, ".git") {
		t.Fatalf("resolveGitDir(plain) = %q, %v", got, err)
	}

	// ".git" file pointing at a gitdir, as used by worktrees and submodules.
	worktree := filepath.Join(root, "worktree")
	realGitDir := filepath.Join(root, "elsewhere", "gitdir")
	if err := os.MkdirAll(realGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(worktree, ".git"), "gitdir: "+realGitDir+"\n")
	if got, err := resolveGitDir(worktree); err != nil || got != realGitDir {
		t.Fatalf("resolveGitDir(worktree) = %q, %v; want %q", got, err, realGitDir)
	}

	// Relative gitdir is resolved against the working tree.
	relative := filepath.Join(root, "relative")
	if err := os.MkdirAll(relative, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(relative, ".git"), "gitdir: ../realgit\n")
	if got, err := resolveGitDir(relative); err != nil || got != filepath.Join(root, "realgit") {
		t.Fatalf("resolveGitDir(relative) = %q, %v; want %q", got, err, filepath.Join(root, "realgit"))
	}
}

func TestReadRefPrefersLooseThenPacked(t *testing.T) {
	gitDir := t.TempDir()
	looseRef := "refs/remotes/origin/main"
	packedRef := "refs/remotes/origin/old"

	looseOID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	packedOID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	writeFile(t, filepath.Join(gitDir, looseRef), looseOID+"\n")
	writeFile(t, filepath.Join(gitDir, "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted \n"+
			packedOID+" "+packedRef+"\n"+
			"^cccccccccccccccccccccccccccccccccccccccc\n")

	if got := readRef(gitDir, looseRef); got != looseOID {
		t.Errorf("readRef(loose) = %q, want %q", got, looseOID)
	}
	if got := readRef(gitDir, packedRef); got != packedOID {
		t.Errorf("readRef(packed) = %q, want %q", got, packedOID)
	}
	if got := readRef(gitDir, "refs/remotes/origin/missing"); got != "" {
		t.Errorf("readRef(missing) = %q, want \"\"", got)
	}
}

func TestReadRefFollowsCommonDir(t *testing.T) {
	// A linked worktree keeps shared refs in the common directory.
	common := t.TempDir()
	worktree := filepath.Join(common, "worktrees", "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(worktree, "commondir"), common+"\n")

	shared := "dddddddddddddddddddddddddddddddddddddddd"
	writeFile(t, filepath.Join(common, "packed-refs"),
		shared+" refs/remotes/origin/main\n")

	if got := readRef(worktree, "refs/remotes/origin/main"); got != shared {
		t.Errorf("readRef via commondir = %q, want %q", got, shared)
	}
	if got := commonDir(worktree); got != common {
		t.Errorf("commonDir = %q, want %q", got, common)
	}
}

func TestReadHEAD(t *testing.T) {
	gitDir := t.TempDir()
	branchOID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	detachedOID := "ffffffffffffffffffffffffffffffffffffffff"

	// Symbolic HEAD that resolves through a branch ref.
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(gitDir, "refs", "heads", "main"), branchOID+"\n")
	if got := readHEAD(gitDir); got != branchOID {
		t.Errorf("readHEAD(symbolic) = %q, want %q", got, branchOID)
	}

	// Symbolic HEAD pointing at a branch that does not exist.
	writeFile(t, filepath.Join(gitDir, "HEAD"), "ref: refs/heads/nope\n")
	if got := readHEAD(gitDir); got != "" {
		t.Errorf("readHEAD(missing branch) = %q, want \"\"", got)
	}

	// Detached HEAD holds the object ID directly.
	writeFile(t, filepath.Join(gitDir, "HEAD"), detachedOID+"\n")
	if got := readHEAD(gitDir); got != detachedOID {
		t.Errorf("readHEAD(detached) = %q, want %q", got, detachedOID)
	}
}

func TestRepositorySizeCountsPacks(t *testing.T) {
	gitDir := t.TempDir()
	writeFile(t, filepath.Join(gitDir, "objects", "pack", "a.pack"), strings.Repeat("x", 100))
	writeFile(t, filepath.Join(gitDir, "objects", "pack", "a.idx"), strings.Repeat("x", 40))
	writeFile(t, filepath.Join(gitDir, "objects", "pack", "a.keep"), strings.Repeat("x", 7))
	writeFile(t, filepath.Join(gitDir, "objects", "info", "packs"), strings.Repeat("x", 999))

	if got := repositorySize(gitDir); got != 140 {
		t.Errorf("repositorySize = %d, want 140 (pack + idx only)", got)
	}
}

func TestIsObjectID(t *testing.T) {
	cases := map[string]bool{
		"0123456789abcdef0123456789abcdef01234567": true,
		strings.Repeat("a", 64):                    true,
		"not-an-object-id":                         false,
		"0123456789abcdef0123456789abcdef0123456":  false, // 39 chars
		"z123456789abcdef0123456789abcdef01234567": false,
	}
	for input, want := range cases {
		if got := isObjectID(input); got != want {
			t.Errorf("isObjectID(%q) = %v, want %v", input, got, want)
		}
	}
}
