package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hxmbl/git-sync/internal/git"
	"github.com/hxmbl/git-sync/internal/model"
)

func FindRepositories(root string) ([]model.Repository, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	repositories := make([]model.Repository, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		gitDir, err := git.ResolveGitDir(path)
		if err != nil {
			return nil, fmt.Errorf("checking %s: %w", entry.Name(), err)
		}
		if gitDir == "" {
			continue
		}
		repositories = append(repositories, model.Repository{
			Name:   entry.Name(),
			Path:   path,
			GitDir: gitDir,
			Size:   git.RepositorySize(gitDir),
		})
	}
	return repositories, nil
}

func FilterRepositories(repositories []model.Repository, only, skip []string) []model.Repository {
	onlySet := make(map[string]bool, len(only))
	for _, name := range only {
		onlySet[name] = true
	}
	skipSet := make(map[string]bool, len(skip))
	for _, name := range skip {
		skipSet[name] = true
	}

	filtered := make([]model.Repository, 0, len(repositories))
	for _, repo := range repositories {
		if len(onlySet) > 0 && !onlySet[repo.Name] {
			continue
		}
		if skipSet[repo.Name] {
			continue
		}
		filtered = append(filtered, repo)
	}
	return filtered
}

// syncRepository performs the network fetch and, only when the upstream ref
// actually moved, a local fast-forward. That removes the second remote round
// trip that "git pull" would otherwise perform after an explicit fetch, and
// skips all update work for repositories that are already current.
func SyncRepository(parent context.Context, repo model.Repository, options model.SyncOptions) (result model.UpdateResult) {
	started := time.Now()
	result.Repository = repo
	defer func() { result.Duration = time.Since(started) }()

	ctx, cancel := context.WithTimeout(parent, options.Timeout)
	defer cancel()

	upstream, upstreamOutput, err := git.UpstreamBranch(ctx, repo.Path)
	if err != nil {
		if git.HasNoUpstream(upstreamOutput) {
			result.Status = model.StatusSkipped
			result.Detail = "has no upstream"
			result.Hint = "Set one with 'git branch --set-upstream-to=<remote>/<branch>'."
			return result
		}
		// Anything else means the directory is not a usable repository, which
		// must not be reported as success.
		result.Status = model.StatusFailed
		result.Detail = "could not be read"
		result.Output = upstreamOutput
		result.Err = err
		if git.IsTimeout(err) {
			result.Detail = "timed out while reading"
			result.Hint = "Increase --timeout or check the local filesystem, then rerun."
		} else {
			result.Hint = "The directory looks like a repository but could not be read. Make sure it is intact, then rerun."
		}
		return result
	}
	if !strings.Contains(upstream, "/") {
		result.Status = model.StatusSkipped
		result.Detail = "has no remote upstream"
		result.Hint = fmt.Sprintf("Upstream %q is not a remote branch.", upstream)
		return result
	}
	remote, branch, _ := strings.Cut(upstream, "/")
	result.Remote, result.Branch = remote, branch

	refname := "refs/remotes/" + upstream
	head := git.ReadHEAD(repo.GitDir)
	if head == "" {
		if oid, err := git.RevParse(ctx, repo.Path, "HEAD"); err == nil {
			head = oid
		}
	}

	output, err := git.Run(ctx, repo.Path, git.FetchCommand(remote, branch, options.AllRefs, options.Tags)...)
	if err != nil {
		result.Status = model.StatusFailed
		result.Detail = "fetch failed"
		result.Output = output
		result.Err = err
		result.Hint = fetchHint(err, output)
		return result
	}

	after := git.ReadRef(repo.GitDir, refname)
	if after == "" {
		after, _ = git.RevParse(ctx, repo.Path, "@{u}")
	}
	if after == "" {
		result.Status = model.StatusFailed
		result.Detail = "could not resolve upstream after fetch"
		result.Output = output
		result.Err = errors.New("could not resolve upstream after fetch")
		result.Hint = "Check the branch's upstream configuration, then rerun."
		return result
	}

	if head == after {
		result.Status = model.StatusUpToDate
		result.Detail = "up to date"
		result.Output = output
		return result
	}

	// Fast-forward only when the local branch is an ancestor of the fetched
	// upstream. Diverged and local-ahead branches are reported, never merged.
	if git.IsAncestor(ctx, repo.Path, head, after) {
		if count, err := git.CommitCount(ctx, repo.Path, head+".."+after); err == nil {
			result.Commits = count
		}

		if options.DryRun {
			result.Status = model.StatusUpdated
			result.DryRun = true
			result.Detail = "would fast-forward " + pluralCommits(result.Commits)
			result.Output = output
			return result
		}

		merged, err := git.FastForward(ctx, repo.Path, after)
		if err != nil {
			result.Status = model.StatusFailed
			result.Detail = updateFailureDetail(merged)
			result.Output = merged
			result.Err = err
			result.Hint = updateHint(merged)
			return result
		}
		result.Status = model.StatusUpdated
		result.Detail = "fast-forwarded " + pluralCommits(result.Commits)
		result.Output = merged
		return result
	}

	if git.IsAncestor(ctx, repo.Path, after, head) {
		// Local has commits that were never pushed; there is nothing to pull.
		count, err := git.CommitCount(ctx, repo.Path, after+".."+head)
		if err != nil {
			count = 0
		}
		result.Commits = count
		result.Status = model.StatusAhead
		result.Detail = fmt.Sprintf("needs push (%s ahead)", pluralCommits(count))
		result.Hint = "Run 'git push' when you're ready."
		result.Output = output
		return result
	}

	result.Status = model.StatusFailed
	result.Detail = "has diverged from upstream"
	result.Output = output
	result.Err = errors.New("branch and upstream have diverged")
	result.Hint = "Rebase or merge manually, then rerun."
	return result
}

func updateFailureDetail(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "would be overwritten"), strings.Contains(lower, "local changes"):
		return "has uncommitted changes"
	case strings.Contains(lower, "conflict"):
		return "has a merge conflict"
	case strings.Contains(lower, "fast-forward"), strings.Contains(lower, "diverged"):
		return "has diverged from upstream"
	default:
		return "could not update"
	}
}

func fetchHint(err error, output string) string {
	if git.IsTimeout(err) {
		return "Timed out. Increase --timeout or check the network connection, then rerun."
	}
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "terminal prompts disabled"),
		strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "could not read username"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "publickey"):
		return "Authentication failed. Configure a credential helper or SSH key, then rerun."
	case strings.Contains(lower, "repository not found"):
		return "Remote repository not found. Check the remote URL and your access, then rerun."
	case strings.Contains(lower, "could not resolve host"),
		strings.Contains(lower, "unable to access"),
		strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "operation timed out"):
		return "Network error. Check the remote URL and your connection, then rerun."
	default:
		return "Check the remote URL, network connection, and authentication, then rerun."
	}
}

func updateHint(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "would be overwritten"), strings.Contains(lower, "local changes"):
		return "Commit or stash your local changes, then rerun."
	case strings.Contains(lower, "fast-forward"), strings.Contains(lower, "diverged"):
		return "The branch and its upstream have diverged. Rebase or merge manually, then rerun."
	default:
		return "Resolve the Git error above, then rerun."
	}
}

func pluralCommits(n int) string {
	word := "commits"
	if n == 1 {
		word = "commit"
	}
	return fmt.Sprintf("%d %s", n, word)
}
