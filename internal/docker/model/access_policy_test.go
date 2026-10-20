package model

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeAccessPorts(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"  ", ""}, {"all", ""}, {" ALL ", ""}, {"1", "1"}, {"65535", "65535"},
		{"443,80,443", "80,443"},
		{"80.443.8443", "80,443,8443"},
		{" 443 . 80 ， 8443,443 ", "80,443,8443"},
		{"80.443.8000-8100.8050", "80,443,8000-8100"},
		{" 0443 ， 80 , 8000 - 8100 ", "80,443,8000-8100"},
		{"8001-8100,8000-8050,8101,80-80", "80,8000-8101"},
		{"1-65535", "1-65535"}, {"443-443", "443"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := NormalizeAccessPorts(tc.input)
			if err != nil || got != tc.want {
				t.Fatalf("ports %q = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
	for _, input := range []string{"0", "65536", "-1", "+443", "https", "*", "443,", ",443", "80,,443", "443.", ".443", "80..443", "80.,443", "80.65536", "443 80", "443-80", "1-2-3", "443-", "-443", "99999999999999999999999999", "１"} {
		t.Run("invalid-"+input, func(t *testing.T) {
			if _, err := NormalizeAccessPorts(input); err == nil {
				t.Fatalf("invalid ports accepted: %q", input)
			}
		})
	}
}

func TestAccessPolicyPortsRoundTripAndIdentity(t *testing.T) {
	var legacy AccessPolicy
	if err := json.Unmarshal([]byte(`{"whitelist":[{"type":"domain","value":"example.com","include_subdomains":true}]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	old := legacy.Whitelist[0]
	policy, err := (AccessPolicy{Whitelist: []AccessEntry{
		old,
		{Type: AccessEntryDomain, Value: "Example.COM.", IncludeSubdomains: true, Ports: "443,80,443"},
		{Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "80,443"},
		{Type: AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "8443"},
	}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.Whitelist) != 3 || policy.Whitelist[0] != old || policy.Whitelist[1].Ports != "80,443" || policy.Whitelist[2].Ports != "8443" {
		t.Fatalf("incorrect port identity or legacy behavior: %+v", policy)
	}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	var restored AccessPolicy
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy, restored) {
		t.Fatalf("ports lost after serialization: %s", data)
	}
	for _, kind := range []string{AccessEntryDomain, AccessEntryIP, AccessEntryCIDR} {
		value := map[string]string{AccessEntryDomain: "example.com", AccessEntryIP: "::1", AccessEntryCIDR: "10.0.0.0/8"}[kind]
		if _, err := (AccessPolicy{Blacklist: []AccessEntry{{Type: kind, Value: value, Ports: "0"}}}).Normalize(); err == nil {
			t.Fatalf("invalid ports accepted for %s", kind)
		}
	}
}
