package terminal

import (
	"bytes"
	"strings"
	"testing"
)

func TestOperationSeparatorColumnMatchesWithAndWithoutColor(t *testing.T) {
	names := []string{"校验 Xray 配置", "Check", "校验 Check", "应用访问策略", "纯文本结果"}
	for _, name := range names {
		plain := FormatOperation(name, "通过")
		plainColumn, ok := SeparatorColumn(plain)
		if !ok || plainColumn != OperationLabelWidth+1 {
			t.Fatalf("%q plain column = %d, line %q", name, plainColumn, plain)
		}
		var raw bytes.Buffer
		WriteOperation(&raw, LevelStep, name, "通过")
		rawColumn, ok := SeparatorColumn(raw.String())
		if !ok || rawColumn != plainColumn {
			t.Fatalf("%q raw column = %d, line %q", name, rawColumn, raw.String())
		}
		var colored bytes.Buffer
		WriteOperation(NewColorWriter(&colored, true), LevelStep, name, "通过")
		coloredColumn, ok := SeparatorColumn(colored.String())
		if !ok || coloredColumn != plainColumn {
			t.Fatalf("%q colored column = %d, line %q", name, coloredColumn, colored.String())
		}
		line := colored.String()
		reset := strings.Index(line, "\x1b[0m")
		bar := strings.Index(line, "│")
		if reset < 0 || bar < reset || strings.Trim(line[reset+len("\x1b[0m"):bar], " ") != "" {
			t.Fatalf("%q padding is inside the color sequence: %q", name, line)
		}
	}
}

func TestOperationContinuationAlignsRightColumn(t *testing.T) {
	var buf bytes.Buffer
	WriteOperation(&buf, LevelError, "操作失败", "第一行\r\n第二行\n")
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	first, ok := SeparatorColumn(lines[0])
	second, ok2 := SeparatorColumn(lines[1])
	if !ok || !ok2 || first != OperationLabelWidth+1 || second != first {
		t.Fatalf("columns = %d %d\n%s", first, second, buf.String())
	}
	if !strings.HasPrefix(lines[1], strings.Repeat(" ", OperationLabelWidth)+" │ 第二行") {
		t.Fatalf("continuation = %q", lines[1])
	}
}

func TestDisplayWidthCountsWideAndNarrowRunes(t *testing.T) {
	if DisplayWidth("校验 Xray 配置") != 14 || DisplayWidth("Check") != 5 || DisplayWidth("…") != 1 || DisplayWidth("€") != 1 || DisplayWidth("│") != 1 {
		t.Fatalf("widths: cjk=%d en=%d ellipsis=%d euro=%d bar=%d", DisplayWidth("校验 Xray 配置"), DisplayWidth("Check"), DisplayWidth("…"), DisplayWidth("€"), DisplayWidth("│"))
	}
}
