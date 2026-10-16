// Package cliui provides small, terminal-aware styles for human CLI output.
// Commands and machine-readable output remain the responsibility of callers.
package cliui

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

func style(output io.Writer, code, text string) string {
	file, ok := output.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || !term.IsTerminal(int(file.Fd())) {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func Bold(output io.Writer, text string) string  { return style(output, "1", text) }
func Muted(output io.Writer, text string) string { return style(output, "2", text) }

func Success(output io.Writer, message string) {
	fmt.Fprintf(output, "%s %s\n", style(output, "32", "✓"), message)
}

func Warning(output io.Writer, message string) {
	fmt.Fprintf(output, "%s %s\n", style(output, "33", "!"), message)
}

func Note(output io.Writer, message string) {
	fmt.Fprintf(output, "  %s\n", Muted(output, message))
}
