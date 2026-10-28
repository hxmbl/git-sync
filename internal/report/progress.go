package report

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hxmbl/git-sync/internal/model"
)

// brailleFrames is the classic rich/pip-style spinner.
var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// progress renders a single in-place "Syncing ⠋ n/total" line on a TTY,
// prints attention lines as they arrive, and restores a clean prompt at the end.
type Progress struct {
	w           io.Writer
	p           Palette
	total       int
	done        int
	interactive bool
	verbose     bool
	nameWidth   int

	mu        sync.Mutex
	frame     int
	drawn     bool
	stopCh    chan struct{}
	stopped   bool
	wg        sync.WaitGroup
	attention int
}

func NewProgress(w io.Writer, p Palette, total, nameWidth int, verbose bool) *Progress {
	return &Progress{
		w:           w,
		p:           p,
		total:       total,
		interactive: isTerminal(w),
		verbose:     verbose,
		nameWidth:   nameWidth,
		stopCh:      make(chan struct{}),
	}
}

func (pr *Progress) Start() {
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

func (pr *Progress) Complete(res model.UpdateResult) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	pr.done++

	if needsAttention(res.Status) {
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
func (pr *Progress) Abort() {
	pr.mu.Lock()
	pr.stopped = true
	pr.clearLocked()
	pr.mu.Unlock()
	close(pr.stopCh)
	pr.wg.Wait()
}

func (pr *Progress) Finish(elapsed time.Duration) {
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

func (pr *Progress) renderLocked() {
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

func (pr *Progress) clearLocked() {
	if !pr.interactive || !pr.drawn {
		return
	}
	fmt.Fprint(pr.w, "\r\033[2K")
	pr.drawn = false
}

func (pr *Progress) writeAttentionLocked(res model.UpdateResult) {
	name := padRight(truncate(sanitize(res.Repository.Name), pr.nameWidth), pr.nameWidth)
	msg := sanitize(attentionMessage(res))
	fmt.Fprintf(pr.w, "%s  %s  %s\n", pr.p.boldYellow("!"), pr.p.bold(name), pr.p.yellow(msg))
	if pr.verbose && res.Output != "" {
		printGitOutput(pr.w, pr.p, res.Output)
	}
	if res.Hint != "" {
		fmt.Fprintf(pr.w, "    %s %s\n", pr.p.dim("↳"), pr.p.dim(sanitize(res.Hint)))
	}
}

func needsAttention(s model.Status) bool {
	switch s {
	case model.StatusFailed, model.StatusAhead, model.StatusSkipped:
		return true
	default:
		return false
	}
}

func attentionMessage(res model.UpdateResult) string {
	if res.Detail != "" {
		return res.Detail
	}
	switch res.Status {
	case model.StatusAhead:
		return "needs push"
	case model.StatusSkipped:
		return "has no upstream"
	default:
		return "needs attention"
	}
}
