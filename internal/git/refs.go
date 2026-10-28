package git

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// resolveGitDir returns the Git directory for a working tree at path, or "" if
// path is not a repository. A ".git" directory is the common case; a ".git"
// file (worktrees and submodules) contains "gitdir: <path>".
func ResolveGitDir(path string) (string, error) {
	info, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if info.IsDir() {
		return filepath.Join(path, ".git"), nil
	}

	data, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		return "", nil
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", nil
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(path, target)
	}
	return filepath.Clean(target), nil
}

// commonDir returns the directory that holds shared refs and objects. For a
// linked worktree the per-worktree git dir contains a "commondir" file
// pointing at the main repository.
func commonDir(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return gitDir
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(gitDir, target)
	}
	return filepath.Clean(target)
}

// readRef resolves a fully qualified ref name to an object ID by reading loose
// refs and packed-refs directly, avoiding a git subprocess. It returns "" when
// the ref cannot be found.
func ReadRef(gitDir, refname string) string {
	common := commonDir(gitDir)

	if data, err := os.ReadFile(filepath.Join(common, filepath.FromSlash(refname))); err == nil {
		if oid := strings.TrimSpace(string(data)); isObjectID(oid) {
			return oid
		}
	}

	f, err := os.Open(filepath.Join(common, "packed-refs"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		oid, name, ok := strings.Cut(line, " ")
		if ok && name == refname {
			return oid
		}
	}
	return ""
}

// readHEAD returns the object ID HEAD points at, following the symbolic ref in
// ".git/HEAD" when the branch exists. It returns "" when HEAD is unreadable.
// HEAD is per-worktree, so it is read from gitDir rather than commonDir.
func ReadHEAD(gitDir string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if refname, ok := strings.CutPrefix(head, "ref:"); ok {
		return ReadRef(gitDir, strings.TrimSpace(refname))
	}
	if isObjectID(head) {
		return head
	}
	return ""
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// repositorySize approximates how much work a fetch will involve, using the
// size of the pack files. It only stats a handful of files, so it is cheap
// enough to use as a scheduling hint.
func RepositorySize(gitDir string) int64 {
	entries, err := os.ReadDir(filepath.Join(commonDir(gitDir), "objects", "pack"))
	if err != nil {
		return 0
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".pack") && !strings.HasSuffix(name, ".idx") {
			continue
		}
		if info, err := entry.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}
