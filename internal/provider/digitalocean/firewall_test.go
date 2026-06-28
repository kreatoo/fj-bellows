package digitalocean

import (
	"context"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

// testResolveAuto returns a fixed CIDR for tests that need the auto sentinel.
func testResolveAuto(context.Context) ([]string, error) {
	return []string{"203.0.113.5/32"}, nil
}

func TestEnsureFirewallCreatesTagTargetedFirewall(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{tag: "prod", client: f, resolveAuto: testResolveAuto, cfg: config{Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}}}
	if err := d.ensureFirewall(context.Background()); err != nil {
		t.Fatalf("ensureFirewall: %v", err)
	}
	if len(f.createFWReqs) != 1 {
		t.Fatalf("CreateFirewall calls = %d, want 1", len(f.createFWReqs))
	}
	req := f.createFWReqs[0]
	if req.Name != "fj-bellows-prod" {
		t.Fatalf("Name = %q", req.Name)
	}
	if len(req.Tags) != 1 || req.Tags[0] != "prod" {
		t.Fatalf("Tags = %#v", req.Tags)
	}
	if len(req.InboundRules) != 1 || req.InboundRules[0].PortRange != "22" {
		t.Fatalf("InboundRules = %#v", req.InboundRules)
	}
	if got := req.InboundRules[0].Sources.Addresses[0]; got != "203.0.113.5/32" {
		t.Fatalf("source = %q", got)
	}
}

func TestEnsureFirewallReusesExisting(t *testing.T) {
	f := &fakeClient{firewall: []godo.Firewall{{ID: "fw-old", Name: "fj-bellows-prod", Tags: []string{"prod"}}}}
	d := &DigitalOcean{tag: "prod", client: f, resolveAuto: testResolveAuto, cfg: config{Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}}}
	if err := d.ensureFirewall(context.Background()); err != nil {
		t.Fatalf("ensureFirewall: %v", err)
	}
	if len(f.createFWReqs) != 0 {
		t.Fatalf("created firewall unexpectedly")
	}
	if len(f.updateFWReqs) != 1 {
		t.Fatalf("UpdateFirewall calls = %d, want 1", len(f.updateFWReqs))
	}
	if d.firewallID != "fw-old" {
		t.Fatalf("firewallID = %q", d.firewallID)
	}
	if got := f.updateFWReqs[0].InboundRules[0].Sources.Addresses[0]; got != "203.0.113.5/32" {
		t.Fatalf("updated source = %q", got)
	}
}

func TestResolveAllowInbound_PassesThroughCIDRs(t *testing.T) {
	got, err := resolveAllowInbound(context.Background(), []string{"198.51.100.1/32", "192.0.2.0/24"}, testResolveAuto)
	if err != nil {
		t.Fatalf("resolveAllowInbound: %v", err)
	}
	want := []string{"198.51.100.1/32", "192.0.2.0/24"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveAllowInbound_AutoSentinel(t *testing.T) {
	got, err := resolveAllowInbound(context.Background(), []string{"auto", "10.0.0.1/32"}, testResolveAuto)
	if err != nil {
		t.Fatalf("resolveAllowInbound: %v", err)
	}
	if len(got) != 2 || got[0] != "203.0.113.5/32" || got[1] != "10.0.0.1/32" {
		t.Fatalf("got %v, want [203.0.113.5/32 10.0.0.1/32]", got)
	}
}

func TestResolveAllowInbound_AnySentinels(t *testing.T) {
	got, err := resolveAllowInbound(context.Background(), []string{"any-v4", "any-v6", "any"}, testResolveAuto)
	if err != nil {
		t.Fatalf("resolveAllowInbound: %v", err)
	}
	want := []string{"0.0.0.0/0", "::/0", "0.0.0.0/0", "::/0"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolveAllowInbound_AutoEmptyError(t *testing.T) {
	_, err := resolveAllowInbound(context.Background(), []string{"auto"}, func(context.Context) ([]string, error) {
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "zero CIDRs") {
		t.Fatalf("expected zero-cidrs error, got %v", err)
	}
}
