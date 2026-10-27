package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// brailleFrames is the classic rich/pip-style spinner.
var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// progress renders a single in-place "Syncing ⠋ n/total" line on a TTY,
// prints attention lines as they arrive, and restores a clean prompt at the end.
type progress struct {
	w           io.Writer
	p           palette
	total       int
	done        int
	interactive bool
	verbose     bool
	nameWidth   int

	mu       sync.Mutex
	frame    int
	drawn    bool
	stopCh   chan struct{}
	stopped  bool
	wg       sync.WaitGroup
	attention int
}

func newProgress(w io.Writer, p palette, total, nameWidth int, verbose bool) *progress {
	return &progress{
		w:           w,
		p:           p,
		total:       total,
		interactive: isTerminal(w),
		verbose:     verbose,
		nameWidth:   nameWidth,
		stopCh:      make(chan struct{}),
	}
}

func (pr *progress) start() {
	if !pr.interactive {
		return
	}
	pr.wg.Add(1)
	go func() {
		defer pr.wg.Done()
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-pr.stopCh:
				return
			case <-ticker.C:
				pr.mu.Lock()
				if !pr.stopped {
					pr.frame = (pr.frame + 1) % len(brailleFrames)
					pr.renderLocked()
				}
				pr.mu.Unlock()
			}
		}
	}()
	pr.mu.Lock()
	pr.renderLocked()
	pr.mu.Unlock()
}

func (pr *progress) complete(res updateResult) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.done++

	if needsAttention(res.status) {
		pr.attention++
		pr.clearLocked()
		pr.writeAttentionLocked(res)
		pr.renderLocked()
		return
	}
	if pr.verbose {
		pr.clearLocked()
		printVerboseResult(pr.w, pr.p, res, pr.nameWidth)
		pr.renderLocked()
	} else if pr.interactive {
		pr.renderLocked()
	}
}

// abort clears the spinner without printing a final status (e.g. on Ctrl-C).
func (pr *progress) abort() {
	pr.mu.Lock()
	pr.stopped = true
	pr.clearLocked()
	pr.mu.Unlock()
	close(pr.stopCh)
	pr.wg.Wait()
}

func (pr *progress) finish(elapsed time.Duration) {
	pr.mu.Lock()
	pr.stopped = true
	pr.clearLocked()
	pr.mu.Unlock()

	close(pr.stopCh)
	pr.wg.Wait()

	pr.mu.Lock()
	defer pr.mu.Unlock()
	if pr.attention == 0 {
		fmt.Fprintf(pr.w, "%s %s\n",
			pr.p.boldGreen("✓"),
			pr.p.bold(fmt.Sprintf("All good · %d synced successfully", pr.total)))
		fmt.Fprintf(pr.w, "  %s\n", pr.p.dim("Finished in "+formatDuration(elapsed)+"."))
		return
	}
	fmt.Fprintf(pr.w, "%s %s\n",
		pr.p.boldYellow("!"),
		pr.p.bold(fmt.Sprintf("Synced %d · %d need attention", pr.total, pr.attention)))
	fmt.Fprintf(pr.w, "  %s\n", pr.p.dim("Finished in "+formatDuration(elapsed)+"."))
}

func (pr *progress) renderLocked() {
	if !pr.interactive || pr.stopped {
		return
	}
	spin := brailleFrames[pr.frame]
	line := fmt.Sprintf("\r%s %s %s",
		pr.p.bold("Syncing"),
		pr.p.cyan(spin),
		pr.p.dim(fmt.Sprintf("%d/%d", pr.done, pr.total)))
	fmt.Fprint(pr.w, line)
	// Pad so a shorter line does not leave stale characters.
	fmt.Fprint(pr.w, "    ")
	pr.drawn = true
}

func (pr *progress) clearLocked() {
	if !pr.interactive || !pr.drawn {
		return
	}
	fmt.Fprint(pr.w, "\r\033[2K")
	pr.drawn = false
}

func (pr *progress) writeAttentionLocked(res updateResult) {
	name := padRight(truncate(sanitize(res.repository.name), pr.nameWidth), pr.nameWidth)
	msg := sanitize(attentionMessage(res))
	fmt.Fprintf(pr.w, "%s  %s  %s\n", pr.p.boldYellow("!"), pr.p.bold(name), pr.p.yellow(msg))
	if pr.verbose && res.output != "" {
		printGitOutput(pr.w, pr.p, res.output)
	}
	if res.hint != "" {
		fmt.Fprintf(pr.w, "    %s %s\n", pr.p.dim("↳"), pr.p.dim(sanitize(res.hint)))
	}
}

func needsAttention(s status) bool {
	switch s {
	case statusFailed, statusAhead, statusSkipped:
		return true
	default:
		return false
	}
}

func attentionMessage(res updateResult) string {
	if res.detail != "" {
		return res.detail
	}
	switch res.status {
	case statusAhead:
		return "needs push"
	case statusSkipped:
		return "has no upstream"
	default:
		return "needs attention"
	}
}
