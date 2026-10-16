package docker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSNIPageBlankInputDoesNotRepeatPlainPrompt(t *testing.T) {
	var out bytes.Buffer
	u := newUI(strings.NewReader("\n\n1\n"), &out)
	action, index, err := u.readSNIPageChoice(1, 0, 1, sniPageOptions{AllowManual: true})
	if err != nil || action != sniPageActionSelect || index != 1 {
		t.Fatalf("choice = %q, %d, %v", action, index, err)
	}
	if strings.Count(out.String(), "❯ 请选择") != 1 || strings.Contains(out.String(), "\033") {
		t.Fatalf("blank input repeated the prompt or emitted terminal controls: %q", out.String())
	}
}

func TestSNICandidateBlankInputRedrawsWithoutRetesting(t *testing.T) {
	for _, retest := range []bool{false, true} {
		name := "select"
		if retest {
			name = "retest"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := deployed(t)
			probes := 0
			m.probeSNI = func(context.Context, []string, string, int) ([]sniCandidate, error) {
				probes++
				return []sniCandidate{{Domain: "www.example.com"}}, nil
			}
			var out dockerTerminalOutput
			m.out, m.errOut = &out, &out
			last := "1\n"
			if retest {
				last = "0\n"
			}
			in := &dockerTerminalInput{answers: []string{"\n", "\n", last}, output: &out}
			u := newUI(in, &out)
			if retest {
				if err := m.retestSNICandidates(context.Background(), u); !errors.Is(err, errCancelled) {
					t.Fatalf("retest exit = %v", err)
				}
			} else {
				domain, err := m.selectSNICandidate(context.Background(), u, "example.com")
				if err != nil || domain != "www.example.com" {
					t.Fatalf("selected SNI = %q, %v", domain, err)
				}
			}
			if probes != 1 || len(in.screens) != 3 {
				t.Fatalf("blank input restarted probes or consumed input: probes=%d prompts=%d", probes, len(in.screens))
			}
			for _, screen := range in.screens {
				if strings.Count(screen, "❯ 请选择") != 1 || !strings.Contains(screen, "www.example.com") {
					t.Fatalf("blank input duplicated the prompt or removed the candidates:\n%s", screen)
				}
			}
		})
	}
}
