package digitalocean

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/digitalocean/godo"
)

// allowInboundSentinels are tokens that resolveAllowInbound expands.
const (
	sentinelAuto  = "auto"
	sentinelAny   = "any"
	sentinelAnyV4 = "any-v4"
	sentinelAnyV6 = "any-v6"
)

func firewallName(tag string) string {
	return "fj-bellows-" + sanitizeName(tag, 63)
}

func (d *DigitalOcean) ensureFirewall(ctx context.Context) error {
	if d.firewallID != "" {
		return nil
	}
	if len(d.resolvedAllowInbound) == 0 {
		var err error
		d.resolvedAllowInbound, err = resolveAllowInbound(ctx, d.cfg.Firewall.AllowInbound, d.resolveAuto)
		if err != nil {
			return fmt.Errorf("digitalocean: resolve allow_inbound: %w", err)
		}
	}
	fws, err := d.client.ListFirewalls(ctx)
	if err != nil {
		return fmt.Errorf("digitalocean: list firewalls: %w", err)
	}
	name := firewallName(d.tag)
	for _, fw := range fws {
		if fw.Name == name && hasString(fw.Tags, d.tag) {
			d.firewallID = fw.ID
			return d.updateFirewall(ctx)
		}
	}
	fw, err := d.client.CreateFirewall(ctx, d.firewallRequest())
	if err != nil {
		return fmt.Errorf("digitalocean: create firewall: %w", err)
	}
	d.firewallID = fw.ID
	return nil
}

func (d *DigitalOcean) updateFirewall(ctx context.Context) error {
	if d.firewallID == "" {
		return nil
	}
	_, err := d.client.UpdateFirewall(ctx, d.firewallID, d.firewallRequest())
	if err != nil {
		return fmt.Errorf("digitalocean: update firewall: %w", err)
	}
	return nil
}

func (d *DigitalOcean) firewallRequest() *godo.FirewallRequest {
	addrs := d.resolvedAllowInbound
	if len(addrs) == 0 {
		addrs = d.cfg.Firewall.AllowInbound
	}
	return &godo.FirewallRequest{
		Name: firewallName(d.tag),
		Tags: []string{d.tag},
		InboundRules: []godo.InboundRule{{
			Protocol:  "tcp",
			PortRange: "22",
			Sources:   &godo.Sources{Addresses: addrs},
		}},
		OutboundRules: []godo.OutboundRule{{
			Protocol:  "tcp",
			PortRange: "all",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}, {
			Protocol:  "udp",
			PortRange: "all",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}, {
			Protocol:     "icmp",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}},
	}
}

// resolveAllowInbound expands sentinel tokens in addrs.  The autoFn parameter
// is called for each "auto" sentinel and must return one or more CIDR strings
// (e.g. the orchestrator's public IPs as /32 or /128 literals).  Non-sentinel
// entries are passed through unchanged.
func resolveAllowInbound(ctx context.Context, addrs []string, autoFn func(context.Context) ([]string, error)) ([]string, error) {
	var out []string
	for _, a := range addrs {
		switch a {
		case sentinelAuto:
			cidrs, err := autoFn(ctx)
			if err != nil {
				return nil, fmt.Errorf("auto sentinel: %w", err)
			}
			if len(cidrs) == 0 {
				return nil, fmt.Errorf("auto sentinel resolved to zero CIDRs")
			}
			out = append(out, cidrs...)
		case sentinelAny:
			out = append(out, "0.0.0.0/0", "::/0")
		case sentinelAnyV4:
			out = append(out, "0.0.0.0/0")
		case sentinelAnyV6:
			out = append(out, "::/0")
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

// defaultResolveAuto resolves the orchestrator's public IPs by querying
// icanhazip.  Returns IPv4/32 and optionally IPv6/128.
func defaultResolveAuto(ctx context.Context) ([]string, error) {
	v4, v4err := probeIP(ctx, "https://ipv4.icanhazip.com")
	v6, v6err := probeIP(ctx, "https://ipv6.icanhazip.com")
	var out []string
	if v4 != "" {
		out = append(out, v4+"/32")
	}
	if v6 != "" {
		out = append(out, v6+"/128")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("both v4 and v6 probes failed: v4=%w, v6=%w", v4err, v6err)
	}
	return out, nil
}

func probeIP(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
