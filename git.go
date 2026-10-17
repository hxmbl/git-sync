package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// gitConfig disables Git's opportunistic background maintenance. Since Git 2.34
// fetch, merge, and rebase trigger incremental repacking by default, which turns
// a fast sync into a series of disk-bound stalls for no benefit here.
var gitConfig = []string{
	"-c", "maintenance.auto=false",
	"-c", "gc.auto=0",
}

// gitEnv keeps a batch run from ever blocking on a credential prompt and makes
// slow or stalled transfers fail quickly instead of holding a worker forever.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_HTTP_LOW_SPEED_LIMIT=1000",
		"GIT_HTTP_LOW_SPEED_TIME=30",
		"GIT_OPTIONAL_LOCKS=0",
	)
}

func runGit(ctx context.Context, directory string, args ...string) (string, error) {
	full := make([]string, 0, len(gitConfig)+len(args))
	full = append(full, gitConfig...)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = directory
	cmd.Env = gitEnv()
	// Bound how long we wait on Git's output pipes. Git spawns helpers such as
	// git-remote-https that inherit those pipes and would otherwise keep us
	// blocked long after the command we care about has been cancelled.
	cmd.WaitDelay = 2 * time.Second
	setProcessGroup(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return string(output), ctxErr
		}
		// The command itself succeeded; only an orphan still holding the pipe
		// caused the delay, which is not a failure of this fetch.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
			return string(output), nil
		}
		return string(output), fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(output), nil
}

// upstreamBranch reports the tracked branch as "remote/branch" (for example
// "origin/main"). The command's output is returned alongside the error so the
// caller can distinguish a branch with no upstream from a repository that
// cannot be read at all.
func upstreamBranch(ctx context.Context, directory string) (string, string, error) {
	output, err := runGit(ctx, directory, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return "", output, err
	}
	return strings.TrimSpace(output), output, nil
}

// hasNoUpstream reports whether an upstream lookup failed only because the
// branch tracks nothing, as opposed to the repository being unusable.
func hasNoUpstream(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "no upstream configured") ||
		strings.Contains(lower, "head does not point to a branch")
}

func fetchCommand(remote, branch string, allRefs, tags bool) []string {
	args := []string{"fetch", "--quiet"}
	if !tags {
		args = append(args, "--no-tags")
	}
	if allRefs {
		args = append(args, remote)
	} else {
		args = append(args, remote, branch)
	}
	return args
}

func revParse(ctx context.Context, directory, rev string) (string, error) {
	output, err := runGit(ctx, directory, "rev-parse", "--verify", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func commitCount(ctx context.Context, directory, revisionRange string) (int, error) {
	output, err := runGit(ctx, directory, "rev-list", "--count", revisionRange)
	if err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		return 0, fmt.Errorf("parsing rev-list output: %w", err)
	}
	return count, nil
}

// isAncestor reports whether ancestor is reachable from descendant. Any error
// (including the normal "not an ancestor" exit status) is treated as false.
func isAncestor(ctx context.Context, directory, ancestor, descendant string) bool {
	_, err := runGit(ctx, directory, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func fastForward(ctx context.Context, directory, oid string) (string, error) {
	return runGit(ctx, directory, "merge", "--ff-only", oid)
}

func isTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
