package docker

import (
	"context"
	"errors"
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
