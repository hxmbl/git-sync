package report

import (
	"io"
	"os"
	"strings"
)

// colorMode controls when ANSI color is emitted.
type colorMode int

const (
	colorAuto colorMode = iota
	colorAlways
	colorNever
)

func parseColorMode(value string) colorMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "always", "true", "yes", "on":
		return colorAlways
	case "never", "false", "no", "off":
		return colorNever
	default:
		return colorAuto
	}
}

// colorEnabled mirrors the conventions most CLIs follow: an explicit mode wins,
// then NO_COLOR, then a usable TERM, then whether the writer is a terminal.
func colorEnabled(mode colorMode, w io.Writer) bool {
	switch mode {
	case colorAlways:
		return true
	case colorNever:
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	switch strings.ToLower(os.Getenv("TERM")) {
	case "", "dumb":
		return false
	}
	return isTerminal(w)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const ansiReset = "\033[0m"

// palette wraps strings in ANSI escapes when color is enabled. When it is not,
// every method is the identity function, so redirected output stays clean.
type Palette struct{ enabled bool }

func (p Palette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

func NewPalette(value string, w io.Writer, disabled bool) Palette {
	mode := parseColorMode(value)
	if disabled {
		mode = colorNever
	}
	return Palette{enabled: colorEnabled(mode, w)}
}

func (p Palette) bold(s string) string       { return p.wrap("\033[1m", s) }
func (p Palette) dim(s string) string        { return p.wrap("\033[2m", s) }
func (p Palette) red(s string) string        { return p.wrap("\033[31m", s) }
func (p Palette) green(s string) string      { return p.wrap("\033[32m", s) }
func (p Palette) yellow(s string) string     { return p.wrap("\033[33m", s) }
func (p Palette) cyan(s string) string       { return p.wrap("\033[36m", s) }
func (p Palette) gray(s string) string       { return p.wrap("\033[90m", s) }
func (p Palette) boldRed(s string) string    { return p.wrap("\033[1;31m", s) }
func (p Palette) boldGreen(s string) string  { return p.wrap("\033[1;32m", s) }
func (p Palette) boldYellow(s string) string { return p.wrap("\033[1;33m", s) }

func (p Palette) Yellow(s string) string { return p.yellow(s) }
