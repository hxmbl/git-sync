package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type repository struct {
	name string
	path string
}

type updateResult struct {
	index       int
	repository  repository
	fetchOutput string
	fetchErr    error
	pullOutput  string
	pullErr     error
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("git-sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	parallelism := flags.Int("j", 6, "number of repositories to update at once")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: git-sync [-j parallelism] [projects-directory]")
		fmt.Fprintln(stderr, "Fetch and pull each Git repository in the directory concurrently.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *parallelism < 1 {
		fmt.Fprintln(stderr, "error: -j must be at least 1")
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, "error: expected at most one projects directory")
		flags.Usage()
		return 2
	}

	root := "."
	if flags.NArg() == 1 {
		root = flags.Arg(0)
	}
	repositories, err := findRepositories(root)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not read projects directory %q: %v\n", root, err)
		return 1
	}
	if len(repositories) == 0 {
		fmt.Fprintf(stdout, "No Git repositories found in %s.\n", root)
		return 0
	}

	workers := *parallelism
	if workers > len(repositories) {
		workers = len(repositories)
	}
	fmt.Fprintf(stdout, "Found %d Git repositories in %s; updating up to %d at a time.\n", len(repositories), root, workers)

	jobs := make(chan updateResult, len(repositories))
	results := make(chan updateResult, len(repositories))
	var outputMu sync.Mutex
	var workerGroup sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			for job := range jobs {
				outputMu.Lock()
				fmt.Fprintf(stdout, "[%d/%d] %s: starting\n", job.index, len(repositories), job.repository.name)
				outputMu.Unlock()
				results <- updateRepository(job)
			}
		}()
	}
	go func() {
		for i, repo := range repositories {
			jobs <- updateResult{index: i + 1, repository: repo}
		}
		close(jobs)
		workerGroup.Wait()
		close(results)
	}()

	succeeded, failed := 0, 0
	for result := range results {
		if result.fetchErr != nil || result.pullErr != nil {
			failed++
		} else {
			succeeded++
		}
		outputMu.Lock()
		printResult(stdout, result, len(repositories))
		outputMu.Unlock()
	}

	fmt.Fprintf(stdout, "\nDone: %d succeeded, %d failed.\n", succeeded, failed)
	if failed > 0 {
		return 1
	}
	return 0
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
		_, err := os.Stat(filepath.Join(path, ".git"))
		if err == nil {
			repositories = append(repositories, repository{name: entry.Name(), path: path})
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("checking %s: %w", entry.Name(), err)
		}
	}
	return repositories, nil
}

func updateRepository(job updateResult) updateResult {
	result := job
	result.fetchOutput, result.fetchErr = runGit(job.repository.path, "fetch")
	if result.fetchErr != nil {
		return result
	}
	result.pullOutput, result.pullErr = runGit(job.repository.path, "pull")
	return result
}

func runGit(directory, subcommand string) (string, error) {
	cmd := exec.Command("git", subcommand)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("git %s: %w", subcommand, err)
	}
	return string(output), nil
}

func printResult(out io.Writer, result updateResult, total int) {
	fmt.Fprintf(out, "[%d/%d] %s\n", result.index, total, result.repository.name)
	printCommandResult(out, "fetch", result.fetchOutput, result.fetchErr)
	if result.fetchErr != nil {
		fmt.Fprintln(out, "  pull: skipped because fetch failed")
		fmt.Fprintln(out, "  action: Check the remote URL, network connection, and authentication, then rerun git-sync.")
		return
	}

	printCommandResult(out, "pull", result.pullOutput, result.pullErr)
	if result.pullErr != nil {
		fmt.Fprintln(out, "  action: Resolve the Git error above (such as local changes or divergent branches), then rerun git-sync.")
	}
}

func printCommandResult(out io.Writer, name, output string, err error) {
	if err != nil {
		fmt.Fprintf(out, "  %s: FAILED (%v)\n", name, err)
	} else {
		fmt.Fprintf(out, "  %s: ok\n", name)
	}
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		for _, line := range strings.Split(trimmed, "\n") {
			fmt.Fprintf(out, "    %s\n", line)
		}
	}
}
