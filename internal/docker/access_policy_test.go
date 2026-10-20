package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestClearWhitelistRetainsAccessRestrictions(t *testing.T) {
	for _, entryPoint := range []string{"cli", "menu"} {
		for _, disabled := range []bool{false, true} {
			name := entryPoint + map[bool]string{false: "/enabled", true: "/disabled"}[disabled]
			t.Run(name, func(t *testing.T) {
				t.Setenv("TERM", "xterm")
				m, _ := deployed(t)
				ctx := context.Background()
				policy := model.AccessPolicy{
					Mode: model.AccessModeWhitelist, Disabled: disabled,
					Whitelist: []model.AccessEntry{{Type: model.AccessEntryDomain, Value: "example.com", Ports: "80,443"}},
					Blacklist: []model.AccessEntry{{Type: model.AccessEntryDomain, Value: "blocked.example.com", Ports: "553"}},
				}
				if err := m.SetPolicy(ctx, policy); err != nil {
					t.Fatal(err)
				}
				if entryPoint == "cli" {
					if err := accessCLI(m, "clear", "white"); err != nil {
						t.Fatal(err)
					}
				} else {
					var out dockerTerminalOutput
					in := &dockerTerminalInput{answers: []string{"3\n", "y\n", "0\n"}, output: &out}
					if err := m.accessListMenu(ctx, newUI(in, &out), "white"); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(out.String(), "保留当前访问模式和开关") || !strings.Contains(out.String(), "访问控制启用时，仅白名单模式将拒绝全部代理请求") {
						t.Fatalf("clear confirmation did not explain the result: %s", out.String())
					}
				}
				state, err := m.LoadState()
				if err != nil {
					t.Fatal(err)
				}
				want := policy
				want.Whitelist = nil
				if !reflect.DeepEqual(state.Node.AccessPolicy, want) {
					t.Fatalf("clearing white changed mode, switch, or blacklist: %+v", state.Node.AccessPolicy)
				}
				root := configRoot(t, readFile(t, m.paths.Config()))
				finalDeny := false
				for _, item := range root["routing"].(map[string]any)["rules"].([]any) {
					rule := item.(map[string]any)
					tag, _ := rule["ruleTag"].(string)
					if rule["outboundTag"] == "access-direct" || strings.HasPrefix(tag, "black-") {
						t.Fatalf("empty whitelist unexpectedly enabled another list: %+v", rule)
					}
					if tag == "access-final" {
						finalDeny = true
						if rule["outboundTag"] != "access-deny" || !reflect.DeepEqual(rule["inboundTag"], []any{state.Node.InboundTag}) || rule["port"] != nil || rule["domain"] != nil || rule["ip"] != nil {
							t.Fatalf("final deny is not unconditional for the proxy inbound: %+v", rule)
						}
					}
				}
				if finalDeny != !disabled {
					t.Fatalf("final-deny=%t, access-control-disabled=%t", finalDeny, disabled)
				}
			})
		}
	}
}

func TestWhitelistSummaryMatchesEffectivePolicy(t *testing.T) {
	for _, white := range []bool{false, true} {
		for _, black := range []bool{false, true} {
			for _, disabled := range []bool{false, true} {
				policy := model.AccessPolicy{Mode: model.AccessModeWhitelist, Disabled: disabled}
				if white {
					policy.Whitelist = []model.AccessEntry{accessEntry(t, "example.com")}
				}
				if black {
					policy.Blacklist = []model.AccessEntry{accessEntry(t, "blocked.example.com")}
				}
				want := "仅允许白名单，其余拒绝"
				if disabled {
					want = "已关闭，名单保留"
				} else if !white {
					want = "白名单为空，拒绝全部代理请求"
				}
				if got := accessPolicySummary(policy); got != want {
					t.Fatalf("incorrect summary for %+v: got %q, want %q", policy, got, want)
				}
			}
		}
	}
}

func TestAccessAutomaticModeWorkflow(t *testing.T) {
	m, _ := deployed(t)
	for _, step := range []struct {
		args []string
		mode string
	}{
		{[]string{"add", "black", "blocked.example.com"}, ""},
		{[]string{"add", "white", "example.com"}, model.AccessModeWhitelist},
		{[]string{"mode", "default"}, model.AccessModeDefault},
		{[]string{"add", "white", "other.example.com", "--ports", "553"}, model.AccessModeDefault},
		{[]string{"clear", "white"}, model.AccessModeDefault},
		{[]string{"add", "white", "example.com"}, model.AccessModeDefault},
		{[]string{"mode", "auto"}, model.AccessModeWhitelist},
		{[]string{"remove", "white", "example.com"}, model.AccessModeWhitelist},
		{[]string{"mode", "auto"}, ""},
		{[]string{"add", "white", "example.com"}, model.AccessModeWhitelist},
		{[]string{"clear", "white"}, model.AccessModeWhitelist},
		{[]string{"mode", "auto"}, ""},
		{[]string{"mode", "default"}, model.AccessModeDefault},
		{[]string{"add", "white", "example.com"}, model.AccessModeDefault},
	} {
		if err := accessCLI(m, step.args...); err != nil {
			t.Fatalf("%v: %v", step.args, err)
		}
		state, err := m.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		if state.Node.AccessPolicy.Mode != step.mode {
			t.Fatalf("%v: mode=%q, want %q", step.args, state.Node.AccessPolicy.Mode, step.mode)
		}
		finalDeny := false
		blacklist := false
		for _, item := range configRoot(t, readFile(t, m.paths.Config()))["routing"].(map[string]any)["rules"].([]any) {
			tag, _ := item.(map[string]any)["ruleTag"].(string)
			finalDeny = finalDeny || tag == "access-final"
			blacklist = blacklist || strings.HasPrefix(tag, "black-")
		}
		wantDeny := step.mode == model.AccessModeWhitelist
		if finalDeny != wantDeny || blacklist == wantDeny {
			t.Fatalf("%v: final-deny=%t blacklist=%t, mode=%q", step.args, finalDeny, blacklist, step.mode)
		}
	}
}

func TestAccessModeMenuRecordsManualDefaultAndRestoresAuto(t *testing.T) {
	entry := model.AccessEntry{Type: model.AccessEntryDomain, Value: "existing.example.com", Ports: "80,443"}
	for _, tc := range []struct {
		name, mode, want, afterAdd string
		answers                    []string
		hasWhite, cancel           bool
	}{
		{name: "manual-default-from-unset", answers: []string{"1\n", "y\n"}, want: model.AccessModeDefault, afterAdd: model.AccessModeDefault},
		{name: "restore-unset", mode: model.AccessModeDefault, answers: []string{"3\n", "y\n"}, afterAdd: model.AccessModeWhitelist},
		{name: "restore-with-whitelist", mode: model.AccessModeDefault, hasWhite: true, answers: []string{"3\n", "y\n"}, want: model.AccessModeWhitelist, afterAdd: model.AccessModeWhitelist},
		{name: "restore-empty-deny", mode: model.AccessModeWhitelist, answers: []string{"3\n", "y\n"}, afterAdd: model.AccessModeWhitelist},
		{name: "cancel-manual-choice", answers: []string{"1\n", "q\n"}, cancel: true, afterAdd: model.AccessModeWhitelist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := deployed(t)
			policy := model.AccessPolicy{Mode: tc.mode}
			if tc.hasWhite {
				policy.Whitelist = []model.AccessEntry{entry}
			}
			if err := m.SetPolicy(context.Background(), policy); err != nil {
				t.Fatal(err)
			}
			var out dockerTerminalOutput
			in := &dockerTerminalInput{answers: tc.answers, output: &out}
			err := m.accessModeMenu(context.Background(), newUI(in, &out))
			if tc.cancel && !errors.Is(err, errCancelled) || !tc.cancel && err != nil {
				t.Fatalf("mode menu result: %v", err)
			}
			state, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			if state.Node.AccessPolicy.Mode != tc.want {
				t.Fatalf("mode=%q, want %q", state.Node.AccessPolicy.Mode, tc.want)
			}
			if !strings.Contains(out.String(), "恢复自动选择") {
				t.Fatal("mode menu did not expose auto restoration")
			}
			if err := accessCLI(m, "add", "white", "new.example.com"); err != nil {
				t.Fatal(err)
			}
			state, err = m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			if state.Node.AccessPolicy.Mode != tc.afterAdd {
				t.Fatalf("manual intent was lost after adding a rule: %+v", state.Node.AccessPolicy)
			}
		})
	}
}

func TestFirstWhitelistMenuSelectsModeWithoutEnablingControl(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		m, _ := deployed(t)
		if err := m.SetPolicy(context.Background(), model.AccessPolicy{Disabled: disabled}); err != nil {
			t.Fatal(err)
		}
		var out dockerTerminalOutput
		in := &dockerTerminalInput{answers: []string{"example.com\n", "1\n", "\n"}, output: &out}
		if err := m.addAccessEntry(context.Background(), newUI(in, &out), "white"); err != nil {
			t.Fatal(err)
		}
		state, err := m.LoadState()
		if err != nil {
			t.Fatal(err)
		}
		if state.Node.AccessPolicy.Mode != model.AccessModeWhitelist || state.Node.AccessPolicy.Disabled != disabled {
			t.Fatalf("automatic mode changed the control switch: %+v", state.Node.AccessPolicy)
		}
	}
}
