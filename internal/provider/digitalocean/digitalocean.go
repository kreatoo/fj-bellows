// Package digitalocean implements the provider.Provider interface for
// DigitalOcean Droplets. It reports per-second billing and manages a
// tag-scoped firewall for ephemeral workers.
package digitalocean

import (
	"context"
	"fmt"
	"time"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"

	"github.com/hstern/fj-bellows/internal/provider"
)

type DigitalOcean struct {
	cfg                  config
	tag                  string
	client               doClient
	newClient            func(token string) doClient
	firewallID           string
	pollInterval         time.Duration
	resolvedAllowInbound []string
	resolveAuto          func(context.Context) ([]string, error)
}

func init() {
	provider.Register("digitalocean", func() provider.Provider { return &DigitalOcean{} })
}

func (d *DigitalOcean) Configure(ctx context.Context, tag string, node yaml.Node) error {
	var cfg config
	if err := node.Decode(&cfg); err != nil {
		return fmt.Errorf("digitalocean: decode provider_config: %w", err)
	}
	cfg.setDefaults()
	if err := cfg.validate(); err != nil {
		return err
	}
	d.cfg = cfg
	d.tag = tag
	if d.newClient == nil {
		d.newClient = newGodoClient
	}
	if d.resolveAuto == nil {
		d.resolveAuto = defaultResolveAuto
	}
	d.client = d.newClient(cfg.Token)
	if err := d.ensureFirewall(ctx); err != nil {
		return err
	}
	d.pollInterval = 2 * time.Second
	return nil
}

func (d *DigitalOcean) BillingModel() provider.BillingModel {
	return provider.BillingPerSecond
}

func newGodoClient(token string) doClient {
	tsrc := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	cl, err := godo.New(oauth2.NewClient(context.Background(), tsrc))
	if err != nil {
		// godo.New only errors on nil http.Client; we always supply one.
		panic(fmt.Sprintf("digitalocean: create godo client: %v", err))
	}
	return &godoClient{client: cl}
}
