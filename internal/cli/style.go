package cli

import (
	"io"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// theme styles text for the terminal. The zero value is plain: no escape
// sequences at all, which is what pipes, NO_COLOR and the golden tests get.
type theme struct {
	color bool
}

var (
	styleBold   = lipgloss.NewStyle().Bold(true)
	styleHeader = lipgloss.NewStyle().Bold(true).Faint(true)
	styleDim    = lipgloss.NewStyle().Faint(true)
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.Green)
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	styleBad    = lipgloss.NewStyle().Foreground(lipgloss.Red).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Cyan)
	styleTitle  = lipgloss.NewStyle().Foreground(lipgloss.Magenta).Bold(true)
)

func (t theme) render(s lipgloss.Style, text string) string {
	if !t.color || text == "" {
		return text
	}
	return s.Render(text)
}

func (t theme) bold(s string) string   { return t.render(styleBold, s) }
func (t theme) header(s string) string { return t.render(styleHeader, s) }
func (t theme) dim(s string) string    { return t.render(styleDim, s) }
func (t theme) ok(s string) string     { return t.render(styleOK, s) }
func (t theme) warn(s string) string   { return t.render(styleWarn, s) }
func (t theme) bad(s string) string    { return t.render(styleBad, s) }
func (t theme) accent(s string) string { return t.render(styleAccent, s) }
func (t theme) title(s string) string  { return t.render(styleTitle, s) }

// level colours text by a three-step level: 0 ok, 1 warn, 2 bad.
func (t theme) level(lvl int, s string) string {
	switch lvl {
	case 2:
		return t.bad(s)
	case 1:
		return t.warn(s)
	default:
		return t.ok(s)
	}
}

// pctLevel maps a usage percentage to a level (75% warn, 90% bad).
func pctLevel(p float64) int {
	switch {
	case p >= 90:
		return 2
	case p >= 75:
		return 1
	default:
		return 0
	}
}

// bar draws a usage meter; it is decoration, so plain output omits it.
func (t theme) bar(p float64, width int) string {
	if !t.color {
		return ""
	}
	p = min(max(p, 0), 100)
	filled := int(p/100*float64(width) + 0.5)
	return t.level(pctLevel(p), strings.Repeat("█", filled)) + t.dim(strings.Repeat("░", width-filled))
}

// column describes one table column.
type column struct {
	title string
	right bool // right-align (numbers)
	max   int  // truncate cells wider than this; 0 means no limit
}

// table renders aligned columns separated by two spaces. Cells may carry
// ANSI styles; widths are measured on the visible text.
type table struct {
	cols   []column
	rows   [][]string
	extras map[int][]string // lines printed under row i
	indent string
}

func newTable(cols ...column) *table { return &table{cols: cols, extras: map[int][]string{}} }

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

// addExtra appends a free-form line under the most recent row.
func (t *table) addExtra(line string) {
	i := len(t.rows) - 1
	t.extras[i] = append(t.extras[i], line)
}

func (t *table) render(w io.Writer, th theme) {
	widths := make([]int, len(t.cols))
	cells := make([][]string, len(t.rows))
	for i, c := range t.cols {
		widths[i] = ansi.StringWidth(c.title)
	}
	for r, row := range t.rows {
		cells[r] = make([]string, len(t.cols))
		for i := range t.cols {
			var s string
			if i < len(row) {
				s = row[i]
			}
			if m := t.cols[i].max; m > 0 && ansi.StringWidth(s) > m {
				s = ansi.Truncate(s, m, "…")
			}
			cells[r][i] = s
			widths[i] = max(widths[i], ansi.StringWidth(s))
		}
	}
	line := func(row []string, style func(string) string) string {
		var b strings.Builder
		b.WriteString(t.indent)
		for i, s := range row {
			pad := strings.Repeat(" ", widths[i]-ansi.StringWidth(s))
			last := i == len(row)-1
			if style != nil {
				s = style(s)
			}
			switch {
			case t.cols[i].right:
				b.WriteString(pad + s)
			case last:
				b.WriteString(s)
			default:
				b.WriteString(s + pad)
			}
			if !last {
				b.WriteString("  ")
			}
		}
		return strings.TrimRight(b.String(), " ") + "\n"
	}
	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = c.title
	}
	_, _ = io.WriteString(w, line(titles, th.header))
	for r, row := range cells {
		_, _ = io.WriteString(w, line(row, nil))
		for _, extra := range t.extras[r] {
			_, _ = io.WriteString(w, t.indent+extra+"\n")
		}
	}
}

// clean removes control characters (including ANSI escapes) from text that
// came from the daemon, so log lines or names cannot drive the terminal.
func clean(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20, r == 0x7f, r >= 0x80 && r < 0xa0:
			return -1
		}
		return r
	}, s)
}
