package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"
	"golang.org/x/text/width"
)

func usageTerminalColumns(out io.Writer) int {
	if file, ok := out.(*os.File); ok {
		if columns, _, err := term.GetSize(int(file.Fd())); err == nil && columns > 0 {
			return columns
		}
	}
	return 80
}

func usageRuneColumns(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	default:
		return 1
	}
}

func usageTextColumns(text string) int {
	columns := 0
	for _, r := range text {
		columns += usageRuneColumns(r)
	}
	return columns
}

// Prefer word boundaries, falling back to a character boundary for long model
// identifiers. Preserve all text, keeping combining marks with their base rune.
func wrapUsageLine(text string, columns int) []string {
	runes := []rune(text)
	lines := []string{}
	for start := 0; start < len(runes); {
		used, end, wordEnd := 0, start, start
		for end < len(runes) {
			r := runes[end]
			size := usageRuneColumns(r)
			if used+size > columns && end > start {
				break
			}
			used += size
			end++
			if unicode.IsSpace(r) {
				wordEnd = end
			}
		}
		if end < len(runes) && wordEnd > start {
			end = wordEnd
		}
		lines = append(lines, string(runes[start:end]))
		start = end
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// Names align left and numeric columns align right. If the numeric columns
// leave too little room for a name, use a two-column block per model instead.
func writeUsageTable(out *strings.Builder, rows [][]string, columns int) {
	sizes := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			sizes[i] = max(sizes[i], usageTextColumns(cell))
		}
	}
	available := columns - 2*(len(sizes)-1)
	for _, size := range sizes[1:] {
		available -= size
	}
	if available < 12 {
		labelWidth, valueWidth := 0, 0
		for i, header := range rows[0][1:] {
			labelWidth = max(labelWidth, usageTextColumns(header))
			valueWidth = max(valueWidth, sizes[i+1])
		}
		for n, row := range rows[1:] {
			if n > 0 {
				out.WriteByte('\n')
			}
			fmt.Fprintln(out, row[0])
			for i, cell := range row[1:] {
				header := rows[0][i+1]
				fmt.Fprintf(out, "  %s%s  %s%s\n", header, strings.Repeat(" ", labelWidth-usageTextColumns(header)), strings.Repeat(" ", valueWidth-usageTextColumns(cell)), cell)
			}
		}
		return
	}
	sizes[0] = min(sizes[0], available)
	for _, row := range rows {
		for line, name := range wrapUsageLine(row[0], sizes[0]) {
			out.WriteString(name)
			if line == 0 {
				out.WriteString(strings.Repeat(" ", sizes[0]-usageTextColumns(name)))
				for i, cell := range row[1:] {
					out.WriteString("  " + strings.Repeat(" ", sizes[i+1]-usageTextColumns(cell)) + cell)
				}
			}
			out.WriteByte('\n')
		}
	}
}
