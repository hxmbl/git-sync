package app

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

	"github.com/hxmbl/git-sync/internal/model"
	"github.com/hxmbl/git-sync/internal/report"
	"github.com/hxmbl/git-sync/internal/syncer"
)

const defaultJobs = 16

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func Run(args []string, stdout, stderr io.Writer, version string) int {
	var (
		jobs        int
		colorChoice string
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

	p := report.NewPalette(colorChoice, stdout, jsonOut)

	repositories, err := syncer.FindRepositories(root)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not read projects directory %q: %v\n\nRun 'git-sync --help' to see all options.\n", root, err)
		return 1
	}
	if len(repositories) == 0 {
		fmt.Fprintf(stdout, "No Git repositories found in %s.\n", report.DisplayDir(root))
		return 0
	}

	repositories = syncer.FilterRepositories(repositories, only, skip)
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
		if repositories[i].Size != repositories[j].Size {
			return repositories[i].Size > repositories[j].Size
		}
		return repositories[i].Name < repositories[j].Name
	})

	nameWidth := 0
	for _, repo := range repositories {
		if l := report.RuneLen(repo.Name); l > nameWidth {
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

	options := model.SyncOptions{DryRun: dryRun, AllRefs: allRefs, Tags: fetchTags, Timeout: timeout, Verbose: verbose}

	var prog *report.Progress
	if !jsonOut {
		prog = report.NewProgress(stdout, p, len(repositories), nameWidth, verbose)
		if dryRun {
			fmt.Fprintf(stdout, "%s\n", p.Yellow("Dry run: nothing will be modified."))
		}
		prog.Start()
	}

	// Cancelling this context tears down every fetch in flight. stop is called
	// as soon as it fires so a second Ctrl-C still kills the process outright.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	started := time.Now()
	jobQueue := make(chan model.Repository)
	resultQueue := make(chan model.UpdateResult)

	var workerGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for repo := range jobQueue {
				resultQueue <- syncer.SyncRepository(ctx, repo, options)
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

	results := make([]model.UpdateResult, 0, len(repositories))
	for result := range resultQueue {
		results = append(results, result)
		if prog != nil && ctx.Err() == nil {
			prog.Complete(result)
		}
	}
	elapsed := time.Since(started)

	if ctx.Err() != nil {
		if prog != nil {
			prog.Abort()
		}
		fmt.Fprintf(stderr, "Interrupted.\n")
		return 130
	}

	if jsonOut {
		if err := report.WriteJSON(stdout, root, results, elapsed, dryRun); err != nil {
			fmt.Fprintf(stderr, "error: writing JSON: %v\n", err)
			return 1
		}
	} else if prog != nil {
		prog.Finish(elapsed)
	}

	for _, result := range results {
		if result.Status == model.StatusFailed {
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
Shows a live progress line; only repositories that need attention are listed.

Flags:
  Essential:
      -j, --jobs int       repositories to sync at once (default 16)
          --color string   color output: auto, always, or never (default "auto")
      -v, --verbose        show every repository, plus git output on failures

  Selection:
          --only string    only sync this repository (repeatable)
          --skip string    skip this repository (repeatable)

  Fetching:
          --all-refs       fetch every branch instead of just the upstream
          --tags           also fetch tags (off by default: --no-tags)
          --timeout dur    per-repository network timeout (default 2m0s)

  Output:
          --json           print machine-readable JSON (for scripts)
          --dry-run        fetch and report, but do not modify anything

  Other:
      -h, --help           show this help
      -V, --version        print the version

Documentation: https://github.com/Hxmbl/git-sync#readme
`
