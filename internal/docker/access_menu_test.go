package docker

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestAccessMenuAddsBatchInOneUpdate(t *testing.T) {
	for _, tc := range []struct {
		name, list, input, match string
		exact                    bool
	}{
		{"white-subdomains", "white", "example.com example.org，1.2.3.4,10.0.0.0/8 example.com", "1", false},
		{"black-exact", "black", "example.com,example.org 1.2.3.4，10.0.0.0/8 example.com", "2", true},
		{"only-addresses", "white", "1.2.3.4 10.0.0.0/8，1.2.3.4", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, fake := deployed(t)
			existing := accessEntry(t, "existing.example.com")
			policy := model.AccessPolicy{Whitelist: []model.AccessEntry{existing}, Blacklist: []model.AccessEntry{existing}}
			if err := m.SetPolicy(context.Background(), policy); err != nil {
				t.Fatal(err)
			}
			fake.calls = nil
			var out dockerTerminalOutput
			answers := []string{tc.input + "\n"}
			if tc.match != "" {
				answers = append(answers, tc.match+"\n")
			}
			in := &dockerTerminalInput{answers: answers, output: &out}
			if err := m.addAccessEntry(context.Background(), newUI(in, &out), tc.list); err != nil {
				t.Fatal(err)
			}
			state, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			added := []model.AccessEntry{
				{Type: model.AccessEntryDomain, Value: "example.com", IncludeSubdomains: !tc.exact},
				{Type: model.AccessEntryDomain, Value: "example.org", IncludeSubdomains: !tc.exact},
				{Type: model.AccessEntryIP, Value: "1.2.3.4"},
				{Type: model.AccessEntryCIDR, Value: "10.0.0.0/8"},
			}
			if tc.match == "" {
				added = added[2:]
			}
			want := append([]model.AccessEntry{existing}, added...)
			got, unchanged := state.Node.AccessPolicy.Whitelist, state.Node.AccessPolicy.Blacklist
			if tc.list == "black" {
				got, unchanged = unchanged, got
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(unchanged, []model.AccessEntry{existing}) {
				t.Fatalf("batch changed the wrong rules: %+v", state.Node.AccessPolicy)
			}
			if in.index != len(answers) {
				t.Fatalf("unexpected input consumption: %d/%d\n%s", in.index, len(answers), out.String())
			}
			wantPrompts := 0
			if tc.match != "" {
				wantPrompts = 1
			}
			if strings.Count(out.String(), "域名及其全部子域名") != wantPrompts {
				t.Fatalf("matching choice was not shown once per batch:\n%s", out.String())
			}
			if count := countDockerCalls(fake, func(call []string) bool { return call[0] == "run" }); count != 1 {
				t.Fatalf("batch validated %d times", count)
			}
			if !strings.Contains(out.String(), "空格、英文逗号或中文逗号") {
				t.Fatal("batch input instructions were not shown")
			}
		})
	}
}

func TestAccessListMenuKeepsALongListOnOneScreen(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	const total = 20
	if err := m.SetPolicy(context.Background(), model.AccessPolicy{Whitelist: accessDomains(t, total)}); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	in := &dockerTerminalInput{answers: []string{"0\n"}, output: &out}
	if err := m.accessListMenu(context.Background(), newUI(in, &out), "white"); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 1 {
		t.Fatalf("prompts = %d", len(in.screens))
	}
	screen := in.screens[0]
	lines := 1 + strings.Count(strings.TrimRight(screen, "\n"), "\n")
	if lines > 30 {
		t.Fatalf("whitelist menu is %d lines:\n%s", lines, screen)
	}
	if !strings.Contains(screen, "20 条 · 第 1/3 页") || !strings.Contains(screen, "h01.example.com") || !strings.Contains(screen, "h09.example.com") {
		t.Fatalf("first page is incomplete:\n%s", screen)
	}
	card := strings.Index(screen, "当前名单")
	closeAt := strings.Index(screen[card:], pageHeaderRule)
	if card < 0 || closeAt < 0 || strings.Contains(screen[card:card+closeAt], "h01.example.com") || strings.Index(screen, "h01.example.com") < card+closeAt {
		t.Fatalf("rules are still inside the summary card:\n%s", screen)
	}
	if strings.Contains(screen, "h10.example.com") || strings.Contains(screen, "规则 1") || strings.Contains(screen, "规则 20") {
		t.Fatalf("first page still lists rules outside the page:\n%s", screen)
	}
	if !strings.Contains(screen, "n 下一页") || !strings.Contains(screen, "按页选择") {
		t.Fatalf("paging controls missing:\n%s", screen)
	}
}

func TestAccessListMenuDeletesRuleOnLaterPage(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	if err := m.SetPolicy(context.Background(), model.AccessPolicy{Whitelist: accessDomains(t, 20)}); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	in := &dockerTerminalInput{answers: []string{"n\n", "2\n", "10\n", "0\n", "0\n"}, output: &out}
	if err := m.accessListMenu(context.Background(), newUI(in, &out), "white"); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 5 {
		t.Fatalf("prompts = %d\n%s", len(in.screens), out.String())
	}
	if strings.Contains(in.screens[1], "h01.example.com") || !strings.Contains(in.screens[1], "第 2/3 页") || !strings.Contains(in.screens[1], "h10.example.com") {
		t.Fatalf("next page did not advance:\n%s", in.screens[1])
	}
	if !strings.Contains(in.screens[2], "删除规则") || !strings.Contains(in.screens[2], "h10.example.com") || strings.Contains(in.screens[2], "h01.example.com") {
		t.Fatalf("delete picker did not open on the current page:\n%s", in.screens[2])
	}
	if strings.Contains(in.screens[3], "h10.example.com") || !strings.Contains(in.screens[3], "h11.example.com") {
		t.Fatalf("deleted rule still on the picker:\n%s", in.screens[3])
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	got := state.Node.AccessPolicy.Whitelist
	if len(got) != 19 || got[9].Value == "h10.example.com" || got[9].Value != "h11.example.com" {
		t.Fatalf("whitelist after delete = %+v", got)
	}
}

func TestAccessListMenuRejectsOffPageDelete(t *testing.T) {
	t.Setenv("TERM", "xterm")
	m, _ := deployed(t)
	if err := m.SetPolicy(context.Background(), model.AccessPolicy{Whitelist: accessDomains(t, 20)}); err != nil {
		t.Fatal(err)
	}
	var out dockerTerminalOutput
	in := &dockerTerminalInput{answers: []string{"2\n", "10\n", "0\n", "0\n"}, output: &out}
	if err := m.accessListMenu(context.Background(), newUI(in, &out), "white"); err != nil {
		t.Fatal(err)
	}
	if len(in.screens) != 4 {
		t.Fatalf("prompts = %d\n%s", len(in.screens), out.String())
	}
	if !strings.Contains(in.screens[2], "本页编号 1 到 9") {
		t.Fatalf("off-page number was accepted:\n%s", in.screens[2])
	}
	state, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Node.AccessPolicy.Whitelist) != 20 {
		t.Fatalf("off-page delete changed the list: %d", len(state.Node.AccessPolicy.Whitelist))
	}
}

func TestParseAccessListInput(t *testing.T) {
	for _, tc := range []struct {
		value, action string
		page, pages   int
		target        int
		ok            bool
	}{
		{value: "0", action: "back", pages: 4, ok: true},
		{value: "q", action: "back", pages: 1, ok: true},
		{value: "1", action: "add", pages: 4, ok: true},
		{value: "2", action: "delete", pages: 4, ok: true},
		{value: "3", action: "clear", pages: 1, ok: true},
		{value: "n", action: "next", page: 0, pages: 4, ok: true},
		{value: "n", page: 3, pages: 4},
		{value: "p", action: "prev", page: 1, pages: 4, ok: true},
		{value: "p", page: 0, pages: 4},
		{value: "g", action: "goto", page: 2, pages: 4, ok: true},
		{value: "G", action: "goto", page: 0, pages: 4, target: 3, ok: true},
		{value: "p3", action: "goto", page: 0, pages: 4, target: 2, ok: true},
		{value: "n", pages: 1},
		{value: "4", pages: 4},
	} {
		action, target, ok := parseAccessListInput(tc.value, tc.page, tc.pages)
		if action != tc.action || target != tc.target || ok != tc.ok {
			t.Fatalf("parse %q page %d/%d = %q,%d,%v want %q,%d,%v", tc.value, tc.page, tc.pages, action, target, ok, tc.action, tc.target, tc.ok)
		}
	}
	action, target, ok := parseAccessDeleteInput("7", 6, 12, 1, 4)
	if action != "pick" || target != 7 || !ok {
		t.Fatalf("delete on page = %q,%d,%v", action, target, ok)
	}
	if _, _, ok = parseAccessDeleteInput("7", 0, 6, 0, 4); ok {
		t.Fatal("off-page delete index was accepted")
	}
}

func accessDomains(t *testing.T, n int) []model.AccessEntry {
	t.Helper()
	entries := make([]model.AccessEntry, n)
	for i := range entries {
		entries[i] = accessEntry(t, fmt.Sprintf("h%02d.example.com", i+1))
	}
	return entries
}

func TestAccessMenuRejectsInvalidBatchWithoutWriting(t *testing.T) {
	m, fake := deployed(t)
	unchanged := savedOperationFiles(t, m)
	fake.calls = nil
	var out dockerTerminalOutput
	in := &dockerTerminalInput{answers: []string{"valid.example.com,https://invalid.example.com 1.2.3.4\n"}, output: &out}
	err := m.addAccessEntry(context.Background(), newUI(in, &out), "white")
	if err == nil || !strings.Contains(err.Error(), "第 2 个目标") {
		t.Fatalf("invalid batch accepted: %v", err)
	}
	unchanged()
	if len(fake.calls) != 0 {
		t.Fatalf("invalid batch called Docker: %v", fake.calls)
	}
}

func TestAccessMenuBatchCancellationDoesNotWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
	}{
		{"input-zero", []string{"0\n"}},
		{"input-q", []string{"q\n"}},
		{"match-zero", []string{"example.com,example.org\n", "0\n"}},
		{"match-q", []string{"example.com,example.org\n", "q\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, fake := deployed(t)
			unchanged := savedOperationFiles(t, m)
			fake.calls = nil
			var out dockerTerminalOutput
			in := &dockerTerminalInput{answers: tc.answers, output: &out}
			if err := m.addAccessEntry(context.Background(), newUI(in, &out), "black"); !errors.Is(err, errCancelled) {
				t.Fatalf("cancel = %v", err)
			}
			unchanged()
			if len(fake.calls) != 0 {
				t.Fatalf("canceled batch called Docker: %v", fake.calls)
			}
		})
	}
}
