package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/hxmbl/git-sync/internal/model"
)

func statusLabel(s model.Status) string {
	switch s {
	case model.StatusUpToDate:
		return "up-to-date"
	case model.StatusUpdated:
		return "fast-forwarded"
	case model.StatusSkipped:
		return "skipped"
	case model.StatusAhead:
		return "ahead"
	default:
		return "failed"
	}
}

func statusGlyph(p Palette, s model.Status) string {
	switch s {
	case model.StatusUpToDate:
		return p.gray("✓")
	case model.StatusUpdated:
		return p.green("↑")
	case model.StatusSkipped:
		return p.yellow("•")
	case model.StatusAhead:
		return p.cyan("⇡")
	default:
		return p.red("✗")
	}
}

// printVerboseResult is the old per-repository line, kept for --verbose.
func printVerboseResult(w io.Writer, p Palette, res model.UpdateResult, nameWidth int) {
	name := truncate(sanitize(res.Repository.Name), nameWidth)
	detail := sanitize(res.Detail)
	switch res.Status {
	case model.StatusUpToDate:
		detail = p.gray(detail)
	case model.StatusUpdated:
		detail = p.green(detail)
	case model.StatusSkipped:
		detail = p.yellow(detail)
	case model.StatusAhead:
		detail = p.cyan(detail)
	default:
		detail = p.red(detail)
	}

	suffix := ""
	if res.Duration > 0 {
		suffix = p.dim("  " + formatDuration(res.Duration))
	}
	fmt.Fprintf(w, "%s  %s  %s%s\n", statusGlyph(p, res.Status), padRight(name, nameWidth), detail, suffix)
	if res.Output != "" && (res.Status == model.StatusFailed || res.Status == model.StatusUpdated) {
		printGitOutput(w, p, res.Output)
	}
}

func printGitOutput(w io.Writer, p Palette, output string) {
	lines := nonEmptyLines(output)
	const maxLines = 20
	for i, line := range lines {
		if i == maxLines {
			fmt.Fprintf(w, "    %s\n", p.dim(fmt.Sprintf("… %d more line(s)", len(lines)-maxLines)))
			break
		}
		fmt.Fprintf(w, "    %s\n", p.dim(sanitize(line)))
	}
}

// --- JSON output ---------------------------------------------------------

type jsonRepository struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	Commits    int    `json:"commits,omitempty"`
	DurationMs int64  `json:"durationMs"`
	Remote     string `json:"remote,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Error      string `json:"error,omitempty"`
}

type jsonSummary struct {
	Total         int   `json:"total"`
	UpToDate      int   `json:"upToDate"`
	FastForwarded int   `json:"fastForwarded"`
	Skipped       int   `json:"skipped"`
	Failed        int   `json:"failed"`
	Ahead         int   `json:"ahead,omitempty"`
	DurationMs    int64 `json:"durationMs"`
}

type jsonReport struct {
	Root         string           `json:"root"`
	DryRun       bool             `json:"dryRun,omitempty"`
	Repositories []jsonRepository `json:"repositories"`
	Summary      jsonSummary      `json:"summary"`
}

func WriteJSON(w io.Writer, root string, results []model.UpdateResult, elapsed time.Duration, dryRun bool) error {
	ordered := append([]model.UpdateResult(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Repository.Name < ordered[j].Repository.Name
	})

	report := jsonReport{Root: root, DryRun: dryRun, Repositories: make([]jsonRepository, 0, len(ordered))}
	for _, res := range ordered {
		entry := jsonRepository{
			Name:       res.Repository.Name,
			Status:     statusLabel(res.Status),
			Detail:     res.Detail,
			Commits:    res.Commits,
			DurationMs: res.Duration.Milliseconds(),
			Remote:     res.Remote,
			Branch:     res.Branch,
		}
		if res.Err != nil {
			entry.Error = res.Err.Error()
		}
		report.Repositories = append(report.Repositories, entry)
		switch res.Status {
		case model.StatusUpToDate:
			report.Summary.UpToDate++
		case model.StatusUpdated:
			report.Summary.FastForwarded++
		case model.StatusSkipped:
			report.Summary.Skipped++
		case model.StatusFailed:
			report.Summary.Failed++
		case model.StatusAhead:
			report.Summary.Ahead++
		}
	}
	report.Summary.Total = len(ordered)
	report.Summary.DurationMs = elapsed.Milliseconds()

	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// --- small helpers -------------------------------------------------------

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// displayDir makes the common "." case read naturally in a sentence.
func DisplayDir(dir string) string {
	if dir == "." {
		return "the current directory"
	}
	return dir
}

func pluralCommits(n int) string {
	return fmt.Sprintf("%d %s", n, plural(n, "commit", "commits"))
}

func formatDuration(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.0fms", float64(d.Microseconds())/1000.0)
	default:
		return fmt.Sprintf("%.0fµs", float64(d.Nanoseconds())/1000.0)
	}
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// sanitize strips control characters so values that come from the filesystem
// or from Git cannot inject escape sequences into the terminal.
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("\\t")
		case r == '\n':
			b.WriteString("\\n")
		case r < 0x20 || r == 0x7f:
			b.WriteString("?")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

func RuneLen(s string) int {
	return len([]rune(s))
}

// padRight aligns on characters rather than bytes, so repository names that
// are not plain ASCII still line up in a column.
func padRight(s string, width int) string {
	if padding := width - RuneLen(s); padding > 0 {
		return s + strings.Repeat(" ", padding)
	}
	return s
}
