package docker

import (
	"context"
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
