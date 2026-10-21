package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

const defaultJobs = 16

type repository struct {
	name   string
	path   string
	gitDir string
	size   int64 // pack size, used only to schedule the largest jobs first
}

type status int

const (
	statusUpToDate status = iota
	statusUpdated
	statusSkipped
	statusFailed
	statusAhead
)

type updateResult struct {
	repository repository
	status     status
	detail     string
	hint       string
	output     string
	err        error
	commits    int
	remote     string
	branch     string
	duration   time.Duration
	dryRun     bool
}

type syncOptions struct {
	dryRun  bool
	allRefs bool
	tags    bool
	timeout time.Duration
	verbose bool
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var (
		jobs        int
		colorChoice string
		quiet       bool
		verbose     bool
		only        stringList
		skip        stringList
		allRefs     bool
		fetchTags   bool
		timeout     time.Duration
		jsonOut     bool
		dryRun      bool
		showHelp    bool
		showVersion bool
	)

	flags := flag.NewFlagSet("git-sync", flag.ContinueOnError)
	flags.SetOutput(io.Discard) // errors are reported below in our own style
	flags.Usage = func() {}     // never dump usage on a parse error; we print a hint instead

	flags.IntVar(&jobs, "j", defaultJobs, "")
	flags.IntVar(&jobs, "jobs", defaultJobs, "")
	flags.StringVar(&colorChoice, "color", "auto", "")
	flags.BoolVar(&quiet, "q", false, "")
	flags.BoolVar(&quiet, "quiet", false, "")
	flags.BoolVar(&verbose, "v", false, "")
	flags.BoolVar(&verbose, "verbose", false, "")
	flags.Var(&only, "only", "")
	flags.Var(&skip, "skip", "")
	flags.BoolVar(&allRefs, "all-refs", false, "")
	flags.BoolVar(&fetchTags, "tags", false, "")
	flags.DurationVar(&timeout, "timeout", 2*time.Minute, "")
	flags.BoolVar(&jsonOut, "json", false, "")
	flags.BoolVar(&dryRun, "dry-run", false, "")
	flags.BoolVar(&showHelp, "h", false, "")
	flags.BoolVar(&showHelp, "help", false, "")
	flags.BoolVar(&showVersion, "V", false, "")
	flags.BoolVar(&showVersion, "version", false, "")

	parsed, err := splitCommandLine(args)
	if err == nil {
		err = flags.Parse(parsed)
	}
	if err != nil {
		return usageError(stderr, err)
	}
	if showHelp {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	if showVersion {
		fmt.Fprintf(stdout, "git-sync %s\n", version)
		return 0
	}

	switch {
	case jobs < 1:
		return usageError(stderr, fmt.Errorf("-j must be at least 1 (got %d)", jobs))
	case timeout <= 0:
		return usageError(stderr, fmt.Errorf("--timeout must be positive (got %s)", timeout))
	case quiet && verbose:
		return usageError(stderr, errors.New("--quiet and --verbose cannot be combined"))
	case flags.NArg() > 1:
		return usageError(stderr, errors.New("expected at most one projects directory"))
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Fprintf(stderr, "error: git was not found on PATH\n\nInstall Git and make sure it is on your PATH, then rerun.\n")
		return 2
	}

	root := "."
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	resolved, err := expandPath(root)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not resolve %q: %v\n\nRun 'git-sync --help' to see all options.\n", root, err)
		return 2
	}
	root = resolved

	mode := parseColorMode(colorChoice)
	if jsonOut {
		mode = colorNever
	}
	p := palette{enabled: colorEnabled(mode, stdout)}

	repositories, err := findRepositories(root)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not read projects directory %q: %v\n\nRun 'git-sync --help' to see all options.\n", root, err)
		return 1
	}
	if len(repositories) == 0 {
		fmt.Fprintf(stdout, "No Git repositories found in %s.\n", displayDir(root))
		return 0
	}

	repositories = filterRepositories(repositories, only, skip)
	if len(repositories) == 0 {
		if len(only) > 0 {
			return usageError(stderr, fmt.Errorf("no repositories match --only %s", strings.Join(only, ", ")))
		}
		fmt.Fprintln(stdout, "No repositories left to sync after applying --skip.")
		return 0
	}

	// Longest-processing-time-first: start the repositories that are likely to
	// take the longest, so they are not the ones left running at the end.
	sort.SliceStable(repositories, func(i, j int) bool {
		if repositories[i].size != repositories[j].size {
			return repositories[i].size > repositories[j].size
		}
		return repositories[i].name < repositories[j].name
	})

	nameWidth := 0
	for _, repo := range repositories {
		if l := runeLen(repo.name); l > nameWidth {
			nameWidth = l
		}
	}
	if nameWidth > 32 {
		nameWidth = 32
	}

	workers := jobs
	if workers > len(repositories) {
		workers = len(repositories)
	}

	if !jsonOut && !quiet {
		header := fmt.Sprintf("Syncing %s in %s",
			p.bold(fmt.Sprintf("%d %s", len(repositories), plural(len(repositories), "repository", "repositories"))),
			p.dim(displayDir(root)))
		if workers < len(repositories) {
			header += " " + p.dim(fmt.Sprintf("(up to %d at a time)", workers))
		}
		fmt.Fprintln(stdout, header)
		if dryRun {
			fmt.Fprintf(stdout, "%s\n", p.yellow("Dry run: nothing will be modified."))
		}
		fmt.Fprintln(stdout)
	}

	options := syncOptions{dryRun: dryRun, allRefs: allRefs, tags: fetchTags, timeout: timeout, verbose: verbose}

	// Cancelling this context tears down every fetch in flight. stop is called
	// as soon as it fires so a second Ctrl-C still kills the process outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	started := time.Now()
	jobQueue := make(chan repository)
	resultQueue := make(chan updateResult)

	var workerGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for repo := range jobQueue {
				resultQueue <- syncRepository(ctx, repo, options)
			}
		}()
	}
	go func() {
	enqueue:
		for _, repo := range repositories {
			select {
			case <-ctx.Done():
				break enqueue
			case jobQueue <- repo:
			}
		}
		close(jobQueue)
		workerGroup.Wait()
		close(resultQueue)
	}()

	results := make([]updateResult, 0, len(repositories))
	for result := range resultQueue {
		results = append(results, result)
		if !jsonOut && ctx.Err() == nil {
			printResult(stdout, p, result, nameWidth, verbose, quiet)
		}
	}
	elapsed := time.Since(started)

	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "\nInterrupted.\n")
		return 130
	}

	if jsonOut {
		if err := writeJSON(stdout, root, results, elapsed, dryRun); err != nil {
			fmt.Fprintf(stderr, "error: writing JSON: %v\n", err)
			return 1
		}
	} else {
		printSummary(stdout, p, results, elapsed, nameWidth, root, quiet)
	}

	for _, result := range results {
		if result.status == statusFailed {
			return 1
		}
	}
	return 0
}

func usageError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "error: %v\n\nRun 'git-sync --help' to see all options.\n", err)
	return 2
}

// valueFlags lists the flags that consume a following argument. It must be
// kept in sync with the flags registered in run.
var valueFlags = map[string]bool{
	"j": true, "jobs": true,
	"color": true,
	"only":  true, "skip": true,
	"timeout": true,
}

// splitCommandLine reorders the command line so the directory can appear
// before or after the flags. Go's flag package stops parsing at the first
// non-flag argument, which would otherwise reject "git-sync ~/Projects -j 4".
// A "--" separator is kept in place so it still ends the flags.
func splitCommandLine(args []string) ([]string, error) {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			options = append(options, arg)
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				continue // value is already attached, e.g. --color=never
			}
			if valueFlags[name] {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag needs an argument: -%s", name)
				}
				i++
				options = append(options, args[i])
			}
			continue
		}
		positional = append(positional, arg)
	}
	return append(options, positional...), nil
}

func findRepositories(root string) ([]repository, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	repositories := make([]repository, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		gitDir, err := resolveGitDir(path)
		if err != nil {
			return nil, fmt.Errorf("checking %s: %w", entry.Name(), err)
		}
		if gitDir == "" {
			continue
		}
		repositories = append(repositories, repository{
			name:   entry.Name(),
			path:   path,
			gitDir: gitDir,
			size:   repositorySize(gitDir),
		})
	}
	return repositories, nil
}

func filterRepositories(repositories []repository, only, skip []string) []repository {
	onlySet := make(map[string]bool, len(only))
	for _, name := range only {
		onlySet[name] = true
	}
	skipSet := make(map[string]bool, len(skip))
	for _, name := range skip {
		skipSet[name] = true
	}

	filtered := make([]repository, 0, len(repositories))
	for _, repo := range repositories {
		if len(onlySet) > 0 && !onlySet[repo.name] {
			continue
		}
		if skipSet[repo.name] {
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
func syncRepository(parent context.Context, repo repository, options syncOptions) (result updateResult) {
	started := time.Now()
	result.repository = repo
	defer func() { result.duration = time.Since(started) }()

	ctx, cancel := context.WithTimeout(parent, options.timeout)
	defer cancel()

	upstream, upstreamOutput, err := upstreamBranch(ctx, repo.path)
	if err != nil {
		if hasNoUpstream(upstreamOutput) {
			result.status = statusSkipped
			result.detail = "skipped (no upstream tracking branch)"
			result.hint = "Set one with 'git branch --set-upstream-to=<remote>/<branch>'."
			return result
		}
		// Anything else means the directory is not a usable repository, which
		// must not be reported as success.
		result.status = statusFailed
		result.detail = "could not read repository"
		result.output = upstreamOutput
		result.err = err
		if isTimeout(err) {
			result.detail = "could not read repository (timed out)"
			result.hint = "Timed out. Increase --timeout or check the local filesystem, then rerun."
		} else {
			result.hint = "The directory looks like a repository but could not be read. Make sure it is intact, then rerun."
		}
		return result
	}
	if !strings.Contains(upstream, "/") {
		result.status = statusSkipped
		result.detail = fmt.Sprintf("skipped (upstream %q is not a remote branch)", upstream)
		return result
	}
	remote, branch, _ := strings.Cut(upstream, "/")
	result.remote, result.branch = remote, branch

	refname := "refs/remotes/" + upstream
	head := readHEAD(repo.gitDir)
	if head == "" {
		if oid, err := revParse(ctx, repo.path, "HEAD"); err == nil {
			head = oid
		}
	}

	output, err := runGit(ctx, repo.path, fetchCommand(remote, branch, options.allRefs, options.tags)...)
	if err != nil {
		result.status = statusFailed
		result.detail = "fetch failed"
		result.output = output
		result.err = err
		result.hint = fetchHint(err, output)
		return result
	}

	after := readRef(repo.gitDir, refname)
	if after == "" {
		after, _ = revParse(ctx, repo.path, "@{u}")
	}
	if after == "" {
		result.status = statusFailed
		result.detail = "fetch failed (could not resolve upstream)"
		result.output = output
		result.err = errors.New("could not resolve upstream after fetch")
		result.hint = "Check the branch's upstream configuration, then rerun."
		return result
	}

	if head == after {
		result.status = statusUpToDate
		result.detail = "up to date"
		result.output = output
		return result
	}

	// Fast-forward only when the local branch is an ancestor of the fetched
	// upstream. Diverged and local-ahead branches are reported, never merged.
	if isAncestor(ctx, repo.path, head, after) {
		if count, err := commitCount(ctx, repo.path, head+".."+after); err == nil {
			result.commits = count
		}

		if options.dryRun {
			result.status = statusUpdated
			result.dryRun = true
			result.detail = "would fast-forward " + pluralCommits(result.commits)
			result.output = output
			return result
		}

		merged, err := fastForward(ctx, repo.path, after)
		if err != nil {
			result.status = statusFailed
			result.detail = "update failed"
			result.output = merged
			result.err = err
			result.hint = updateHint(merged)
			return result
		}
		result.status = statusUpdated
		result.detail = "fast-forwarded " + pluralCommits(result.commits)
		result.output = merged
		return result
	}

	if isAncestor(ctx, repo.path, after, head) {
		// Local has commits that were never pushed; there is nothing to pull.
		count, err := commitCount(ctx, repo.path, after+".."+head)
		if err != nil {
			count = 0
		}
		result.status = statusUpToDate
		result.detail = fmt.Sprintf("up to date (local ahead by %s)", pluralCommits(count))
		result.output = output
		return result
	}

	result.status = statusFailed
	result.detail = "update failed (diverged)"
	result.output = output
	result.err = errors.New("branch and upstream have diverged")
	result.hint = "The branch and its upstream have diverged. Rebase or merge manually, then rerun."
	return result
}

func fetchHint(err error, output string) string {
	if isTimeout(err) {
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

func expandPath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

const usageText = `Usage:
  git-sync [flags] [projects-directory]

Fetch and fast-forward each Git repository in a directory, concurrently.
Each repository is fetched once, then fast-forwarded only if its upstream
moved; repositories that are already current do no extra work.

Flags:
  Essential:
      -j, --jobs int       repositories to sync at once (default 16)
          --color string   color output: auto, always, or never (default "auto")
      -q, --quiet          only show failures and the summary
      -v, --verbose        show git output and timings for every repository

  Selection:
          --only string    only sync this repository (repeatable)
          --skip string    skip this repository (repeatable)

  Fetching:
          --all-refs       fetch every branch instead of just the upstream
          --tags           also fetch tags (off by default: --no-tags)
          --timeout dur    per-repository network timeout (default 2m0s)

  Output:
          --json           print machine-readable JSON
          --dry-run        fetch and report, but do not modify anything

  Other:
      -h, --help           show this help
      -V, --version        print the version

Documentation: https://github.com/Hxmbl/git-sync#readme
`
