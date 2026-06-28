package digitalocean

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func decodeConfigForTest(t *testing.T, in string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(in), &n); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	return *n.Content[0]
}

func TestConfigureRequiresFields(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{name: "token", yaml: `region: nyc3
size: s-2vcpu-4gb
image: debian-12-x64
firewall: {allow_inbound: [auto]}
`, wantSub: "token"},
		{name: "region", yaml: `token: t
size: s-2vcpu-4gb
image: debian-12-x64
firewall: {allow_inbound: [auto]}
`, wantSub: "region"},
		{name: "size", yaml: `token: t
region: nyc3
image: debian-12-x64
firewall: {allow_inbound: [auto]}
`, wantSub: "size"},
		{name: "image", yaml: `token: t
region: nyc3
size: s-2vcpu-4gb
firewall: {allow_inbound: [auto]}
`, wantSub: "image"},
		{name: "firewall", yaml: `token: t
region: nyc3
size: s-2vcpu-4gb
image: debian-12-x64
`, wantSub: "firewall.allow_inbound"},
		{name: "refresh_interval_too_short", yaml: `token: t
region: nyc3
size: s-2vcpu-4gb
image: debian-12-x64
firewall: {allow_inbound: [auto], refresh_interval: 30s}
`, wantSub: "refresh_interval"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &DigitalOcean{newClient: func(string) doClient { return &fakeClient{} }}
			err := p.Configure(context.Background(), "prod", decodeConfigForTest(t, c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("Configure error = %v, want substring %q", err, c.wantSub)
			}
		})
	}
}

func TestConfigureDefaultsFirewallRefreshInterval(t *testing.T) {
	p := &DigitalOcean{newClient: func(string) doClient { return &fakeClient{} }}
	err := p.Configure(context.Background(), "prod", decodeConfigForTest(t, `token: t
region: nyc3
size: s-2vcpu-4gb
image: debian-12-x64
firewall: {allow_inbound: [203.0.113.5/32]}
`))
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if p.cfg.Firewall.RefreshInterval != time.Hour {
		t.Fatalf("RefreshInterval = %s, want 1h", p.cfg.Firewall.RefreshInterval)
	}
}
