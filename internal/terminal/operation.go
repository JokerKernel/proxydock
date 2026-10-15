package terminal

import (
	"io"
	"strings"
	"unicode"
)

// Level is the severity of one operation line. Color is applied after the
// label has been padded, so the separator stays in the same column.
type Level int

const (
	LevelInfo Level = iota
	LevelStep
	LevelResult
	LevelWarn
	LevelError
)

// Phase groups operation lines. A blank line separates different phases.
const (
	PhaseCheck  = 1
	PhaseBackup = 2
	PhaseApply  = 3
	PhaseResult = 4
)

// OperationLabelWidth is the fixed display width of the left column.
// Wide characters (CJK) count as two columns and narrow characters as one.
const OperationLabelWidth = 16

// WriteOperation writes one two-column operation line. Continuation lines of a
// multi-line detail start at the right column. Color, when enabled, is added
// only after the plain-text padding is calculated.
func WriteOperation(w io.Writer, level Level, name, detail string) {
	if w == nil {
		return
	}
	lines := splitDetail(detail)
	enabled := colorEnabled(w)
	var b strings.Builder
	for i, line := range lines {
		label := ""
		if i == 0 {
			label = name
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderLabel(label, level, enabled))
		b.WriteString(" │ ")
		b.WriteString(line)
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(w, b.String())
}

// DisplayWidth reports the terminal column width of s.
func DisplayWidth(s string) int {
	width := 0
	for _, r := range s {
		width += runeWidth(r)
	}
	return width
}

func splitDetail(detail string) []string {
	detail = strings.ReplaceAll(detail, "\r\n", "\n")
	detail = strings.TrimRight(detail, "\n")
	if detail == "" {
		return []string{""}
	}
	return strings.Split(detail, "\n")
}

func renderLabel(name string, level Level, enabled bool) string {
	width := DisplayWidth(name)
	pad := 0
	if width < OperationLabelWidth {
		pad = OperationLabelWidth - width
	}
	shown := name
	if enabled && name != "" {
		shown = wrapANSI(levelColor(level), name)
	}
	return shown + strings.Repeat(" ", pad)
}

func levelColor(level Level) string {
	switch level {
	case LevelStep:
		return ansiBoldOrange
	case LevelResult:
		return ansiBoldGreen
	case LevelWarn:
		return ansiBoldYellow
	case LevelError:
		return ansiBoldRed
	default:
		return ansiBlue
	}
}

func colorEnabled(w io.Writer) bool {
	if colored, ok := w.(*colorWriter); ok {
		return colored.enabled
	}
	return terminalColorEnabled(w)
}

func runeWidth(r rune) int {
	if r < 0x20 || r == 0x7f || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func isWideRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F:
		return true
	case r >= 0x2329 && r <= 0x232A:
		return true
	case r >= 0x2E80 && r <= 0xA4CF:
		return true
	case r >= 0xAC00 && r <= 0xD7A3:
		return true
	case r >= 0xF900 && r <= 0xFAFF:
		return true
	case r >= 0xFE10 && r <= 0xFE19:
		return true
	case r >= 0xFE30 && r <= 0xFE6F:
		return true
	case r >= 0xFF01 && r <= 0xFF60:
		return true
	case r >= 0xFFE0 && r <= 0xFFE6:
		return true
	case r >= 0x1F300 && r <= 0x1FAFF:
		return true
	case r >= 0x20000 && r <= 0x3FFFD:
		return true
	default:
		return false
	}
}
