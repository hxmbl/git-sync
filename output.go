package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func statusGlyph(p palette, s status) string {
	switch s {
	case statusUpToDate:
		return p.gray("✓")
	case statusUpdated:
		return p.green("↑")
	case statusSkipped:
		return p.yellow("•")
	case statusAhead:
		return p.cyan("⇡")
	default:
		return p.red("✗")
	}
}

func statusLabel(s status) string {
	switch s {
	case statusUpToDate:
		return "up-to-date"
	case statusUpdated:
		return "fast-forwarded"
	case statusSkipped:
		return "skipped"
	case statusAhead:
		return "ahead"
	default:
		return "failed"
	}
}

func printResult(w io.Writer, p palette, res updateResult, nameWidth int, verbose, quiet bool) {
	if quiet && res.status != statusFailed {
		return
	}

	name := truncate(sanitize(res.repository.name), nameWidth)
	detail := sanitize(res.detail)
	switch res.status {
	case statusUpToDate:
		detail = p.gray(detail)
	case statusUpdated:
		detail = p.green(detail)
	case statusSkipped:
		detail = p.yellow(detail)
	case statusAhead:
		detail = p.cyan(detail)
	default:
		detail = p.red(detail)
	}

	suffix := ""
	if verbose && res.duration > 0 {
		suffix = p.dim("  " + formatDuration(res.duration))
	}
	fmt.Fprintf(w, "%s  %s  %s%s\n", statusGlyph(p, res.status), padRight(name, nameWidth), detail, suffix)

	if verbose || res.status == statusFailed {
		printGitOutput(w, p, res.output)
	}
	if res.hint != "" && (res.status == statusFailed || verbose) {
		fmt.Fprintf(w, "    %s %s\n", p.cyan("↳"), sanitize(res.hint))
	}
}

func printGitOutput(w io.Writer, p palette, output string) {
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

func printSummary(w io.Writer, p palette, results []updateResult, elapsed time.Duration, nameWidth int, root string, quiet bool) {
	var up, updated, skipped, failed, ahead int
	var failedRepos []updateResult
	var aheadRepos []updateResult
	for _, res := range results {
		switch res.status {
		case statusUpToDate:
			up++
		case statusUpdated:
			updated++
		case statusSkipped:
			skipped++
		case statusFailed:
			failed++
			failedRepos = append(failedRepos, res)
		case statusAhead:
			ahead++
			aheadRepos = append(aheadRepos, res)
		}
	}

	fmt.Fprintln(w)

	if updated == 0 && skipped == 0 && failed == 0 && ahead == 0 {
		if p.enabled {
			box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("42")).Padding(0, 2)
			content := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")).Render("✓ All systems synced") + "\n" +
				lipgloss.NewStyle().Faint(true).Render(fmt.Sprintf("%d %s up to date • Checked in %s", up, plural(up, "repository", "repositories"), formatDuration(elapsed)))
			fmt.Fprintln(w, box.Render(content))
			return
		}
		fmt.Fprintf(w, "%s All %d %s up to date.\n", p.green("✓"), up, plural(up, "repository", "repositories"))
		fmt.Fprintf(w, "  %s\n", p.dim("Checked in "+formatDuration(elapsed)+"."))
		return
	}

	if p.enabled {
		titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 3)
		var b strings.Builder
		b.WriteString(titleStyle.Render("Summary") + "\n")
		parts := make([]string, 0, 5)
		if up > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(fmt.Sprintf("✓ %d up to date", up)))
		}
		if ahead > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Render(fmt.Sprintf("⇡ %d ahead", ahead)))
		}
		if updated > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(fmt.Sprintf("↑ %d fast-forwarded", updated)))
		}
		if skipped > 0 {
			parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(fmt.Sprintf("• %d skipped", skipped)))
		}
		if failed > 0 {
			parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render(fmt.Sprintf("✗ %d failed", failed)))
		}
		b.WriteString(strings.Join(parts, "  ·  ") + "\n")
		b.WriteString(lipgloss.NewStyle().Faint(true).Render("Finished in "+formatDuration(elapsed)+".") + "\n")

		if len(aheadRepos) > 0 {
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("45")).Render(fmt.Sprintf("⚡ Needs push (%d)", len(aheadRepos))) + "\n")
			for _, res := range aheadRepos {
				b.WriteString(fmt.Sprintf("  %s  %s\n", padRight(truncate(sanitize(res.repository.name), nameWidth), nameWidth), lipgloss.NewStyle().Faint(true).Render(sanitize(res.detail))))
			}
			cmd := pushCommand(aheadRepos, root)
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Faint(true).Render("Push:") + "\n")
			b.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("45")).Render(cmd) + "\n")
		}

		if len(failedRepos) > 0 {
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")).Render(fmt.Sprintf("❗ Needs attention (%d)", len(failedRepos))) + "\n")
			for _, res := range failedRepos {
				b.WriteString(fmt.Sprintf("  %s  %s\n", padRight(truncate(sanitize(res.repository.name), nameWidth), nameWidth), lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Render(sanitize(res.detail))))
			}
			b.WriteString("\n")
			b.WriteString(lipgloss.NewStyle().Faint(true).Render("Retry:") + "\n")
			b.WriteString("  " + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(retryCommand(failedRepos, root)) + "\n")
		}
		fmt.Fprint(w, box.Render(b.String())+"\n")
		return
	}

	writeSectionTitle(w, p, "Summary")

	parts := make([]string, 0, 5)
	if up > 0 {
		parts = append(parts, p.gray(fmt.Sprintf("%d up to date", up)))
	}
	if ahead > 0 {
		parts = append(parts, p.cyan(fmt.Sprintf("%d ahead of remote", ahead)))
	}
	if updated > 0 {
		parts = append(parts, p.green(fmt.Sprintf("%d fast-forwarded", updated)))
	}
	if skipped > 0 {
		parts = append(parts, p.yellow(fmt.Sprintf("%d skipped", skipped)))
	}
	if failed > 0 {
		parts = append(parts, p.red(fmt.Sprintf("%d failed", failed)))
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(parts, p.dim("  ·  ")))
	fmt.Fprintf(w, "  %s\n", p.dim("Finished in "+formatDuration(elapsed)+"."))

	if len(aheadRepos) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", p.cyan(fmt.Sprintf("%d %s need to push:", len(aheadRepos), plural(len(aheadRepos), "repository", "repositories"))))
		for _, res := range aheadRepos {
			fmt.Fprintf(w, "    %s  %s\n",
				padRight(truncate(sanitize(res.repository.name), nameWidth), nameWidth),
				p.cyan(sanitize(res.detail)))
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Push them:")
		fmt.Fprintf(w, "      %s\n", p.dim(pushCommand(aheadRepos, root)))
	}

	if len(failedRepos) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s\n", p.red(fmt.Sprintf("%d %s %s attention:", len(failedRepos), plural(len(failedRepos), "repository", "repositories"), plural(len(failedRepos), "needs", "need"))))
		for _, res := range failedRepos {
			fmt.Fprintf(w, "    %s  %s\n",
				padRight(truncate(sanitize(res.repository.name), nameWidth), nameWidth),
				p.red(sanitize(res.detail)))
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "  Retry the failed repositories:")
		fmt.Fprintf(w, "      %s\n", p.dim(retryCommand(failedRepos, root)))
	}
}

func writeSectionTitle(w io.Writer, p palette, title string) {
	dashes := 50 - len(title)
	if dashes < 3 {
		dashes = 3
	}
	fmt.Fprintln(w, p.dim(strings.Repeat("─", 8)+" "+title+" "+strings.Repeat("─", dashes)))
}

func pushCommand(results []updateResult, root string) string {
	if len(results) == 1 {
		res := results[0]
		remote := res.remote
		branch := res.branch
		if remote == "" {
			remote = "origin"
		}
		if branch == "" {
			branch = res.repository.name // fallback
		}
		return fmt.Sprintf("git -C %s push %s %s", shellQuote(res.repository.path), shellQuote(remote), shellQuote(branch))
	}
	var b strings.Builder
	b.WriteString("for repo in")
	for _, res := range results {
		b.WriteString(" ")
		b.WriteString(shellQuote(res.repository.path))
	}
	b.WriteString("; do (cd \"$repo\" && git push) || true; done")
	return b.String()
}

func retryCommand(results []updateResult, root string) string {
	var b strings.Builder
	b.WriteString("git-sync")
	for _, res := range results {
		b.WriteString(" --only ")
		b.WriteString(shellQuote(res.repository.name))
	}
	if root != "." {
		b.WriteString(" ")
		b.WriteString(shellQuote(root))
	}
	return b.String()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}()<>|&;!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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

func writeJSON(w io.Writer, root string, results []updateResult, elapsed time.Duration, dryRun bool) error {
	ordered := append([]updateResult(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].repository.name < ordered[j].repository.name
	})

	report := jsonReport{Root: root, DryRun: dryRun, Repositories: make([]jsonRepository, 0, len(ordered))}
	for _, res := range ordered {
		entry := jsonRepository{
			Name:       res.repository.name,
			Status:     statusLabel(res.status),
			Detail:     res.detail,
			Commits:    res.commits,
			DurationMs: res.duration.Milliseconds(),
			Remote:     res.remote,
			Branch:     res.branch,
		}
		if res.err != nil {
			entry.Error = res.err.Error()
		}
		report.Repositories = append(report.Repositories, entry)
		switch res.status {
		case statusUpToDate:
			report.Summary.UpToDate++
		case statusUpdated:
			report.Summary.FastForwarded++
		case statusSkipped:
			report.Summary.Skipped++
		case statusFailed:
			report.Summary.Failed++
		case statusAhead:
			if report.Summary.Ahead == 0 && report.Summary.FastForwarded == 0 && report.Summary.Skipped == 0 && report.Summary.Failed == 0 && report.Summary.UpToDate >= 0 {
				// allow
			}
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
func displayDir(dir string) string {
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

func runeLen(s string) int {
	return len([]rune(s))
}

// padRight aligns on characters rather than bytes, so repository names that
// are not plain ASCII still line up in a column.
func padRight(s string, width int) string {
	if padding := width - runeLen(s); padding > 0 {
		return s + strings.Repeat(" ", padding)
	}
	return s
}
