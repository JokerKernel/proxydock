package docker

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"proxydock/internal/docker/model"
)

func TestAccessCLIPortsDefaultsIdentityAndRemoval(t *testing.T) {
	m, _ := deployed(t)
	legacy := accessEntry(t, "example.com")
	if err := m.SetPolicy(context.Background(), model.AccessPolicy{Whitelist: []model.AccessEntry{legacy}}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "white", "example.com", "10.0.0.0/8", "2001:db8::1"},
		{"add", "white", "example.com", "--ports", "8443"},
		{"add", "white", "example.com", "--ports", "443,80,443"},
		{"add", "white", "example.com", "--ports", "443.80.443"},
		{"add", "white", "example.com", "--ports", "all"},
	} {
		if err := accessCLI(m, args...); err != nil {
			t.Fatal(err)
		}
	}
	s, err := m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	want := []model.AccessEntry{
		legacy,
		{Type: model.AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "80,443"},
		{Type: model.AccessEntryCIDR, Value: "10.0.0.0/8"},
		{Type: model.AccessEntryIP, Value: "2001:db8::1"},
		{Type: model.AccessEntryDomain, Value: "example.com", IncludeSubdomains: true, Ports: "8443"},
	}
	if !reflect.DeepEqual(s.Node.AccessPolicy.Whitelist, want) {
		t.Fatalf("wrong defaults or duplicate handling: %+v", s.Node.AccessPolicy.Whitelist)
	}
	if err := accessCLI(m, "remove", "white", "example.com", "--ports", "443.80"); err != nil {
		t.Fatal(err)
	}
	if err := m.Generate(context.Background(), model.GenerateOptions{}, false); err != nil {
		t.Fatal(err)
	}
	s, err = m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	want = append(want[:1], want[2:]...)
	if !reflect.DeepEqual(s.Node.AccessPolicy.Whitelist, want) {
		t.Fatalf("removal or regeneration changed unrelated ports: %+v", s.Node.AccessPolicy.Whitelist)
	}
	if err := accessCLI(m, "remove", "white", "example.com", "--ports", "all"); err != nil {
		t.Fatal(err)
	}
	s, err = m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Node.AccessPolicy.Whitelist, want[1:]) {
		t.Fatalf("all-ports removal removed a restricted rule: %+v", s.Node.AccessPolicy.Whitelist)
	}
	for _, args := range [][]string{
		{"add", "black", "blocked.example.com", "--exact"},
		{"add", "black", "1.2.3.4", "--ports", "8000-8100"},
	} {
		if err := accessCLI(m, args...); err != nil {
			t.Fatal(err)
		}
	}
	s, err = m.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Node.AccessPolicy.Blacklist, []model.AccessEntry{
		{Type: model.AccessEntryDomain, Value: "blocked.example.com", Ports: "80,443"},
		{Type: model.AccessEntryIP, Value: "1.2.3.4", Ports: "8000-8100"},
	}) {
		t.Fatalf("wrong blacklist port defaults: %+v", s.Node.AccessPolicy.Blacklist)
	}
}

func TestAccessMenuSetsEachAddressPorts(t *testing.T) {
	for _, listName := range []string{"white", "black"} {
		t.Run(listName, func(t *testing.T) {
			t.Setenv("TERM", "xterm")
			m, _ := deployed(t)
			var out dockerTerminalOutput
			in := &dockerTerminalInput{answers: []string{
				"a.example.com b.example.com c.example.com 10.0.0.0/8\n", "2\n",
				"443\n", "\n", "all\n", "8100.8000-8100\n",
			}, output: &out}
			if err := m.addAccessEntry(context.Background(), newUI(in, &out), listName); err != nil {
				t.Fatal(err)
			}
			s, err := m.LoadState()
			if err != nil {
				t.Fatal(err)
			}
			got := s.Node.AccessPolicy.Whitelist
			if listName == "black" {
				got = s.Node.AccessPolicy.Blacklist
			}
			want := []model.AccessEntry{
				{Type: model.AccessEntryDomain, Value: "a.example.com", Ports: "443"},
				{Type: model.AccessEntryDomain, Value: "b.example.com", Ports: "80,443"},
				{Type: model.AccessEntryDomain, Value: "c.example.com"},
				{Type: model.AccessEntryCIDR, Value: "10.0.0.0/8", Ports: "8000-8100"},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ports were shared between addresses: %+v", got)
			}
			for _, entry := range got {
				if entry.Ports != "" && !strings.Contains(accessEntryLabel(entry), entry.Ports) {
					t.Fatalf("list label hides the port condition: %s", accessEntryLabel(entry))
				}
			}
		})
	}
}

func TestAccessMenuInvalidPortsDoesNotWrite(t *testing.T) {
	m, fake := deployed(t)
	unchanged := savedOperationFiles(t, m)
	fake.calls = nil
	var out dockerTerminalOutput
	in := &dockerTerminalInput{answers: []string{"a.example.com b.example.com\n", "1\n", "443\n", "65536\n"}, output: &out}
	if err := m.addAccessEntry(context.Background(), newUI(in, &out), "white"); err == nil {
		t.Fatal("invalid ports accepted")
	}
	unchanged()
	if len(fake.calls) != 0 {
		t.Fatalf("invalid ports called Docker: %v", fake.calls)
	}
}
