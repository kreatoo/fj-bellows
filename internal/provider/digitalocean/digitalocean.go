// Package digitalocean implements the provider.Provider interface for
// DigitalOcean Droplets. It reports per-second billing and manages a
// tag-scoped firewall for ephemeral workers.
package digitalocean

import (
	"context"
	"fmt"
	"sync"
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
	firewallMu           sync.Mutex
	firewallLastRefresh  time.Time
	pollInterval         time.Duration
	resolvedAllowInbound []string
	resolveAuto          func(context.Context) ([]string, error)
	sshKeyMu             sync.Mutex
	sshKeyID             int
	sshKeyValue          string
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
	// Configure may be called again by tests or a future reload path. Do not
	// retain a firewall identity or resolved ingress addresses from another
	// deployment/configuration.
	d.firewallID = ""
	d.firewallLastRefresh = time.Time{}
	d.resolvedAllowInbound = nil
	d.sshKeyID = 0
	d.sshKeyValue = ""
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
	hc := oauth2.NewClient(context.Background(), tsrc)
	// godo otherwise inherits an http.Client with no response deadline. Keep a
	// stalled DigitalOcean endpoint from wedging reconciliation forever.
	hc.Timeout = 30 * time.Second
	cl, err := godo.New(hc)
	if err != nil {
		// godo.New only errors on nil http.Client; we always supply one.
		panic(fmt.Sprintf("digitalocean: create godo client: %v", err))
	}
	return &godoClient{client: cl}
}
