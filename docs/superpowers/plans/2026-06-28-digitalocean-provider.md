# DigitalOcean Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `digitalocean` provider that provisions real Droplets with managed firewall protection for ephemeral Forgejo Actions workers.

**Architecture:** Implement `internal/provider/digitalocean` as a focused provider package using the official `godo` SDK behind narrow interfaces and hand-written fakes. Reuse existing orchestrator cloud-init, SSH dispatch, per-second teardown, and config expansion; DigitalOcean-specific work stays inside `provider_config`.

**Tech Stack:** Go, `github.com/digitalocean/godo`, existing `provider.Provider`, existing `bootstrap.Render`, existing `SSHDispatcher`, YAML config.

---

## File Structure

- Create `internal/provider/digitalocean/README.md`: operator documentation, config shape, token scopes, billing, firewall behavior.
- Create `internal/provider/digitalocean/digitalocean.go`: provider type, registration, `Configure`, `BillingModel`, client wiring.
- Create `internal/provider/digitalocean/config.go`: YAML config structs, validation, defaults, managed firewall config.
- Create `internal/provider/digitalocean/client.go`: narrow interfaces wrapping `godo` Droplets, SSHKeys, Firewalls, Tags.
- Create `internal/provider/digitalocean/sshkey.go`: deterministic SSH key import/reuse from `spec.AuthorizedKey`.
- Create `internal/provider/digitalocean/firewall.go`: managed firewall ensure/update/reap and `auto` CIDR resolution.
- Create `internal/provider/digitalocean/provision.go`: Droplet create, readiness polling, conversion to `provider.Instance`.
- Create `internal/provider/digitalocean/list.go`: tag-filtered Droplet listing.
- Create `internal/provider/digitalocean/destroy.go`: Droplet delete and last-Droplet firewall reap.
- Create `internal/provider/digitalocean/info.go`: optional operator debug info.
- Create focused tests beside each file, using in-package fakes only.
- Modify `cmd/fj-bellows/main.go`: blank-import `internal/provider/digitalocean`.
- Modify `config.example.yaml`: add commented DigitalOcean example.
- Modify `go.mod`/`go.sum`: add `github.com/digitalocean/godo`.

---

### Task 1: Add Package Skeleton, Registration, Config, and Docs

**Files:**
- Create: `internal/provider/digitalocean/README.md`
- Create: `internal/provider/digitalocean/config.go`
- Create: `internal/provider/digitalocean/digitalocean.go`
- Test: `internal/provider/digitalocean/config_test.go`
- Test: `internal/provider/digitalocean/digitalocean_test.go`

- [ ] **Step 1: Add dependency**

Run:

```sh
go get github.com/digitalocean/godo@latest
```

Expected: `go.mod` and `go.sum` update.

- [ ] **Step 2: Write failing registration and config tests**

Create `internal/provider/digitalocean/config_test.go`:

```go
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
```

Create `internal/provider/digitalocean/digitalocean_test.go`:

```go
package digitalocean

import (
	"testing"

	"github.com/hstern/fj-bellows/internal/provider"
)

func TestRegistered(t *testing.T) {
	p, err := provider.New("digitalocean")
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	if p == nil {
		t.Fatal("provider.New returned nil")
	}
}

func TestBillingModelIsPerSecond(t *testing.T) {
	if got := (&DigitalOcean{}).BillingModel(); got != provider.BillingPerSecond {
		t.Fatalf("BillingModel = %v, want %v", got, provider.BillingPerSecond)
	}
}
```

- [ ] **Step 3: Run tests and verify they fail**

Run:

```sh
go test ./internal/provider/digitalocean -run 'TestConfigure|TestRegistered|TestBillingModel' -count=1 -v
```

Expected: FAIL because package/types do not exist.

- [ ] **Step 4: Add minimal package implementation**

Create `internal/provider/digitalocean/config.go`:

```go
package digitalocean

import (
	"errors"
	"time"
)

type config struct {
	Token    string         `yaml:"token"`
	Region   string         `yaml:"region"`
	Size     string         `yaml:"size"`
	Image    string         `yaml:"image"`
	Firewall firewallConfig `yaml:"firewall"`
}

type firewallConfig struct {
	AllowInbound    []string      `yaml:"allow_inbound"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

func (c *config) setDefaults() {
	if c.Firewall.RefreshInterval == 0 {
		c.Firewall.RefreshInterval = time.Hour
	}
}

func (c config) validate() error {
	if c.Token == "" {
		return errors.New("digitalocean: provider_config missing: token")
	}
	if c.Region == "" {
		return errors.New("digitalocean: provider_config missing: region")
	}
	if c.Size == "" {
		return errors.New("digitalocean: provider_config missing: size")
	}
	if c.Image == "" {
		return errors.New("digitalocean: provider_config missing: image")
	}
	if len(c.Firewall.AllowInbound) == 0 {
		return errors.New("digitalocean: provider_config missing: firewall.allow_inbound")
	}
	if c.Firewall.RefreshInterval < time.Minute {
		return errors.New("digitalocean: firewall.refresh_interval must be >= 1m")
	}
	return nil
}
```

Create `internal/provider/digitalocean/digitalocean.go`:

```go
package digitalocean

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"

	"github.com/hstern/fj-bellows/internal/provider"
)

type DigitalOcean struct {
	cfg       config
	tag       string
	client    doClient
	newClient func(token string) doClient
}

func init() {
	provider.Register("digitalocean", func() provider.Provider { return &DigitalOcean{} })
}

func (d *DigitalOcean) Configure(_ context.Context, tag string, node yaml.Node) error {
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
	d.client = d.newClient(cfg.Token)
	return nil
}

func (d *DigitalOcean) BillingModel() provider.BillingModel {
	return provider.BillingPerSecond
}

func newGodoClient(token string) doClient {
	tsrc := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return &godoClient{client: godo.New(oauth2.NewClient(context.Background(), tsrc))}
}
```

Create `internal/provider/digitalocean/client.go` with the temporary client type needed by the initial tests:

```go
package digitalocean

import "github.com/digitalocean/godo"

type doClient interface{}

type godoClient struct {
	client *godo.Client
}

type fakeClient struct{}
```

Create `internal/provider/digitalocean/README.md`:

```markdown
# internal/provider/digitalocean

DigitalOcean implementation of `provider.Provider`, built on the official
`godo` SDK.

`provider_config` shape:

```yaml
provider_config:
  token: ${DIGITALOCEAN_TOKEN}
  region: nyc3
  size: s-2vcpu-4gb
  image: debian-12-x64
  firewall:
    allow_inbound:
      - auto
    refresh_interval: 1h
```

DigitalOcean is treated as per-second billed; use low `poll.idle_timeout` for
one-job-per-Droplet behavior.
```

- [ ] **Step 5: Run tests and verify they pass**

Run:

```sh
go test ./internal/provider/digitalocean -run 'TestConfigure|TestRegistered|TestBillingModel' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add go.mod go.sum internal/provider/digitalocean
git commit -m "feat(digitalocean): add provider skeleton and config"
```

---

### Task 2: Add godo Client Interfaces and Fakes

**Files:**
- Modify: `internal/provider/digitalocean/client.go`
- Create: `internal/provider/digitalocean/fake_test.go`
- Test: `internal/provider/digitalocean/client_test.go`

- [ ] **Step 1: Write interface compile tests**

Create `internal/provider/digitalocean/client_test.go`:

```go
package digitalocean

import "testing"

func TestGodoClientSatisfiesInterfaces(t *testing.T) {
	var c any = &godoClient{}
	if _, ok := c.(dropletClient); !ok {
		t.Fatal("godoClient must satisfy dropletClient")
	}
	if _, ok := c.(keyClient); !ok {
		t.Fatal("godoClient must satisfy keyClient")
	}
	if _, ok := c.(firewallClient); !ok {
		t.Fatal("godoClient must satisfy firewallClient")
	}
	if _, ok := c.(tagClient); !ok {
		t.Fatal("godoClient must satisfy tagClient")
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
go test ./internal/provider/digitalocean -run TestGodoClientSatisfiesInterfaces -count=1 -v
```

Expected: FAIL because interfaces are undefined.

- [ ] **Step 3: Implement narrow interfaces and godo adapters**

Replace `internal/provider/digitalocean/client.go` with:

```go
package digitalocean

import (
	"context"

	"github.com/digitalocean/godo"
)

type doClient interface {
	dropletClient
	keyClient
	firewallClient
	tagClient
}

type dropletClient interface {
	CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error)
	GetDroplet(ctx context.Context, id int) (*godo.Droplet, error)
	ListDropletsByTag(ctx context.Context, tag string) ([]godo.Droplet, error)
	DeleteDroplet(ctx context.Context, id int) error
}

type keyClient interface {
	ListKeys(ctx context.Context) ([]godo.Key, error)
	CreateKey(ctx context.Context, req *godo.KeyCreateRequest) (*godo.Key, error)
}

type firewallClient interface {
	ListFirewalls(ctx context.Context) ([]godo.Firewall, error)
	CreateFirewall(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, error)
	UpdateFirewall(ctx context.Context, id string, req *godo.FirewallRequest) (*godo.Firewall, error)
	DeleteFirewall(ctx context.Context, id string) error
}

type tagClient interface {
	CreateTag(ctx context.Context, name string) error
}

type godoClient struct {
	client *godo.Client
}

func (c *godoClient) CreateDroplet(ctx context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	d, _, err := c.client.Droplets.Create(ctx, req)
	return d, err
}

func (c *godoClient) GetDroplet(ctx context.Context, id int) (*godo.Droplet, error) {
	d, _, err := c.client.Droplets.Get(ctx, id)
	return d, err
}

func (c *godoClient) ListDropletsByTag(ctx context.Context, tag string) ([]godo.Droplet, error) {
	var out []godo.Droplet
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := c.client.Droplets.ListByTag(ctx, tag, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		pageNum, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opt.Page = pageNum + 1
	}
	return out, nil
}

func (c *godoClient) DeleteDroplet(ctx context.Context, id int) error {
	_, err := c.client.Droplets.Delete(ctx, id)
	return err
}

func (c *godoClient) ListKeys(ctx context.Context) ([]godo.Key, error) {
	var out []godo.Key
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := c.client.Keys.List(ctx, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		pageNum, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opt.Page = pageNum + 1
	}
	return out, nil
}

func (c *godoClient) CreateKey(ctx context.Context, req *godo.KeyCreateRequest) (*godo.Key, error) {
	k, _, err := c.client.Keys.Create(ctx, req)
	return k, err
}

func (c *godoClient) ListFirewalls(ctx context.Context) ([]godo.Firewall, error) {
	var out []godo.Firewall
	opt := &godo.ListOptions{PerPage: 200}
	for {
		page, resp, err := c.client.Firewalls.List(ctx, opt)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if resp == nil || resp.Links == nil || resp.Links.IsLastPage() {
			break
		}
		pageNum, err := resp.Links.CurrentPage()
		if err != nil {
			return nil, err
		}
		opt.Page = pageNum + 1
	}
	return out, nil
}

func (c *godoClient) CreateFirewall(ctx context.Context, req *godo.FirewallRequest) (*godo.Firewall, error) {
	fw, _, err := c.client.Firewalls.Create(ctx, req)
	return fw, err
}

func (c *godoClient) UpdateFirewall(ctx context.Context, id string, req *godo.FirewallRequest) (*godo.Firewall, error) {
	fw, _, err := c.client.Firewalls.Update(ctx, id, req)
	return fw, err
}

func (c *godoClient) DeleteFirewall(ctx context.Context, id string) error {
	_, err := c.client.Firewalls.Delete(ctx, id)
	return err
}

func (c *godoClient) CreateTag(ctx context.Context, name string) error {
	_, _, err := c.client.Tags.Create(ctx, &godo.TagCreateRequest{Name: name})
	return err
}
```

Create `internal/provider/digitalocean/fake_test.go`:

```go
package digitalocean

import (
	"context"
	"sync"

	"github.com/digitalocean/godo"
)

type fakeClient struct {
	mu sync.Mutex

	droplets []godo.Droplet
	keys     []godo.Key
	firewall []godo.Firewall

	createDropletReqs []*godo.DropletCreateRequest
	createKeyReqs     []*godo.KeyCreateRequest
	createFWReqs      []*godo.FirewallRequest
	updateFWReqs      []*godo.FirewallRequest
	deletedDroplets   []int
	deletedFirewalls  []string
}

func (f *fakeClient) CreateDroplet(_ context.Context, req *godo.DropletCreateRequest) (*godo.Droplet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createDropletReqs = append(f.createDropletReqs, req)
	d := godo.Droplet{ID: 100 + len(f.createDropletReqs), Name: req.Name, Tags: req.Tags}
	f.droplets = append(f.droplets, d)
	return &d, nil
}

func (f *fakeClient) GetDroplet(_ context.Context, id int) (*godo.Droplet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.droplets {
		if d.ID == id {
			dd := d
			return &dd, nil
		}
	}
	return &godo.Droplet{ID: id}, nil
}

func (f *fakeClient) ListDropletsByTag(_ context.Context, tag string) ([]godo.Droplet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []godo.Droplet
	for _, d := range f.droplets {
		for _, t := range d.Tags {
			if t == tag {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (f *fakeClient) DeleteDroplet(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedDroplets = append(f.deletedDroplets, id)
	return nil
}

func (f *fakeClient) ListKeys(context.Context) ([]godo.Key, error) { return f.keys, nil }

func (f *fakeClient) CreateKey(_ context.Context, req *godo.KeyCreateRequest) (*godo.Key, error) {
	f.createKeyReqs = append(f.createKeyReqs, req)
	k := godo.Key{ID: 500 + len(f.createKeyReqs), Name: req.Name, PublicKey: req.PublicKey}
	f.keys = append(f.keys, k)
	return &k, nil
}

func (f *fakeClient) ListFirewalls(context.Context) ([]godo.Firewall, error) { return f.firewall, nil }

func (f *fakeClient) CreateFirewall(_ context.Context, req *godo.FirewallRequest) (*godo.Firewall, error) {
	f.createFWReqs = append(f.createFWReqs, req)
	fw := godo.Firewall{ID: "fw-1", Name: req.Name, Tags: req.Tags}
	f.firewall = append(f.firewall, fw)
	return &fw, nil
}

func (f *fakeClient) UpdateFirewall(_ context.Context, id string, req *godo.FirewallRequest) (*godo.Firewall, error) {
	f.updateFWReqs = append(f.updateFWReqs, req)
	return &godo.Firewall{ID: id, Name: req.Name, Tags: req.Tags}, nil
}

func (f *fakeClient) DeleteFirewall(_ context.Context, id string) error {
	f.deletedFirewalls = append(f.deletedFirewalls, id)
	return nil
}

func (f *fakeClient) CreateTag(context.Context, string) error { return nil }
```

- [ ] **Step 4: Run tests**

Run:

```sh
go test ./internal/provider/digitalocean -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/provider/digitalocean go.mod go.sum
git commit -m "feat(digitalocean): add godo client adapters"
```

---

### Task 3: Implement SSH Key Import/Re-use

**Files:**
- Create: `internal/provider/digitalocean/sshkey.go`
- Test: `internal/provider/digitalocean/sshkey_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/provider/digitalocean/sshkey_test.go`:

```go
package digitalocean

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

const testAuthorizedKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey fj-bellows"

func TestEnsureSSHKeyReusesExistingPublicKey(t *testing.T) {
	f := &fakeClient{keys: []godo.Key{{ID: 77, Name: "other", PublicKey: testAuthorizedKey}}}
	d := &DigitalOcean{tag: "prod", client: f}
	id, err := d.ensureSSHKey(context.Background(), testAuthorizedKey)
	if err != nil {
		t.Fatalf("ensureSSHKey: %v", err)
	}
	if id != 77 {
		t.Fatalf("id = %d, want 77", id)
	}
	if len(f.createKeyReqs) != 0 {
		t.Fatalf("created key unexpectedly: %+v", f.createKeyReqs)
	}
}

func TestEnsureSSHKeyCreatesDeterministicName(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{tag: "prod", client: f}
	id, err := d.ensureSSHKey(context.Background(), testAuthorizedKey)
	if err != nil {
		t.Fatalf("ensureSSHKey: %v", err)
	}
	if id == 0 {
		t.Fatal("id = 0")
	}
	if len(f.createKeyReqs) != 1 {
		t.Fatalf("CreateKey calls = %d, want 1", len(f.createKeyReqs))
	}
	if got := f.createKeyReqs[0].Name; got != "fj-bellows-prod" {
		t.Fatalf("Name = %q, want fj-bellows-prod", got)
	}
	if got := f.createKeyReqs[0].PublicKey; got != testAuthorizedKey {
		t.Fatalf("PublicKey = %q", got)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
go test ./internal/provider/digitalocean -run TestEnsureSSHKey -count=1 -v
```

Expected: FAIL because `ensureSSHKey` is undefined.

- [ ] **Step 3: Implement SSH key handling**

Create `internal/provider/digitalocean/sshkey.go`:

```go
package digitalocean

import (
	"context"
	"fmt"
	"strings"

	"github.com/digitalocean/godo"
)

func (d *DigitalOcean) ensureSSHKey(ctx context.Context, authorizedKey string) (int, error) {
	want := strings.TrimSpace(authorizedKey)
	keys, err := d.client.ListKeys(ctx)
	if err != nil {
		return 0, fmt.Errorf("digitalocean: list ssh keys: %w", err)
	}
	for _, k := range keys {
		if strings.TrimSpace(k.PublicKey) == want {
			return k.ID, nil
		}
	}
	k, err := d.client.CreateKey(ctx, &godo.KeyCreateRequest{
		Name:      sshKeyName(d.tag),
		PublicKey: want,
	})
	if err != nil {
		return 0, fmt.Errorf("digitalocean: create ssh key: %w", err)
	}
	return k.ID, nil
}

func sshKeyName(tag string) string {
	return "fj-bellows-" + sanitizeName(tag, 64)
}
```

Create helper in `internal/provider/digitalocean/names.go`:

```go
package digitalocean

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func sanitizeName(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "default"
	}
	if len(out) <= max {
		return out
	}
	sum := sha256.Sum256([]byte(out))
	suffix := hex.EncodeToString(sum[:])[:8]
	keep := max - len(suffix) - 1
	return strings.TrimRight(out[:keep], "-") + "-" + suffix
}
```

- [ ] **Step 4: Run tests**

Run:

```sh
go test ./internal/provider/digitalocean -run TestEnsureSSHKey -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/provider/digitalocean
git commit -m "feat(digitalocean): import or reuse ssh keys"
```

---

### Task 4: Implement Managed Firewall Ensure/Reap

**Files:**
- Create: `internal/provider/digitalocean/firewall.go`
- Test: `internal/provider/digitalocean/firewall_test.go`

- [ ] **Step 1: Write failing firewall tests**

Create `internal/provider/digitalocean/firewall_test.go`:

```go
package digitalocean

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

func TestEnsureFirewallCreatesTagTargetedFirewall(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{tag: "prod", client: f, cfg: config{Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}}}
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
	if len(req.InboundRules) != 1 || req.InboundRules[0].Ports != "22" {
		t.Fatalf("InboundRules = %#v", req.InboundRules)
	}
	if got := req.InboundRules[0].Sources.Addresses[0]; got != "203.0.113.5/32" {
		t.Fatalf("source = %q", got)
	}
}

func TestEnsureFirewallReusesExisting(t *testing.T) {
	f := &fakeClient{firewall: []godo.Firewall{{ID: "fw-old", Name: "fj-bellows-prod", Tags: []string{"prod"}}}}
	d := &DigitalOcean{tag: "prod", client: f, cfg: config{Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}}}
	if err := d.ensureFirewall(context.Background()); err != nil {
		t.Fatalf("ensureFirewall: %v", err)
	}
	if len(f.createFWReqs) != 0 {
		t.Fatalf("created firewall unexpectedly")
	}
	if d.firewallID != "fw-old" {
		t.Fatalf("firewallID = %q", d.firewallID)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
go test ./internal/provider/digitalocean -run TestEnsureFirewall -count=1 -v
```

Expected: FAIL because `ensureFirewall` is undefined.

- [ ] **Step 3: Add firewall implementation**

Modify `DigitalOcean` in `digitalocean.go` to include:

```go
	firewallID string
```

Create `internal/provider/digitalocean/firewall.go`:

```go
package digitalocean

import (
	"context"
	"fmt"

	"github.com/digitalocean/godo"
)

func firewallName(tag string) string {
	return "fj-bellows-" + sanitizeName(tag, 63)
}

func (d *DigitalOcean) ensureFirewall(ctx context.Context) error {
	if d.firewallID != "" {
		return nil
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
	return &godo.FirewallRequest{
		Name: firewallName(d.tag),
		Tags: []string{d.tag},
		InboundRules: []godo.InboundRule{{
			Protocol:  "tcp",
			Ports:     "22",
			Sources:   &godo.Sources{Addresses: d.cfg.Firewall.AllowInbound},
		}},
		OutboundRules: []godo.OutboundRule{{
			Protocol:     "tcp",
			Ports:        "all",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}, {
			Protocol:     "udp",
			Ports:        "all",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}, {
			Protocol:     "icmp",
			Destinations: &godo.Destinations{Addresses: []string{"0.0.0.0/0", "::/0"}},
		}},
	}
}

func hasString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
```

Modify `Configure` in `digitalocean.go` after client creation:

```go
	if err := d.ensureFirewall(context.Background()); err != nil {
		return err
	}
```

If passing `context.Background()` here is undesirable, change the `Configure` receiver parameter name from `_ context.Context` to `ctx context.Context` and call `d.ensureFirewall(ctx)`.

- [ ] **Step 4: Run tests**

Run:

```sh
go test ./internal/provider/digitalocean -run TestEnsureFirewall -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/provider/digitalocean
git commit -m "feat(digitalocean): manage deployment firewall"
```

---

### Task 5: Implement Provision, Polling, and Instance Conversion

**Files:**
- Create: `internal/provider/digitalocean/provision.go`
- Test: `internal/provider/digitalocean/provision_test.go`

- [ ] **Step 1: Write failing provision tests**

Create `internal/provider/digitalocean/provision_test.go`:

```go
package digitalocean

import (
	"context"
	"testing"
	"time"

	"github.com/digitalocean/godo"

	"github.com/hstern/fj-bellows/internal/provider"
)

func TestProvisionCreatesDropletWithCloudInitTagAndSSHKey(t *testing.T) {
	created := time.Date(2026, 6, 28, 5, 0, 0, 0, time.UTC)
	f := &fakeClient{keys: []godo.Key{{ID: 77, PublicKey: testAuthorizedKey}}}
	d := &DigitalOcean{tag: "prod", client: f, cfg: config{Region: "nyc3", Size: "s-2vcpu-4gb", Image: "debian-12-x64"}, pollInterval: time.Millisecond}
	f.droplets = []godo.Droplet{{ID: 101, Name: "prod-abc", Tags: []string{"prod"}, Created: created.Format(time.RFC3339), Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.10"}}}}}
	inst, err := d.Provision(context.Background(), provider.Spec{Tag: "prod", Name: "prod-abc", UserData: "#cloud-config", AuthorizedKey: testAuthorizedKey})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(f.createDropletReqs) != 1 {
		t.Fatalf("CreateDroplet calls = %d", len(f.createDropletReqs))
	}
	req := f.createDropletReqs[0]
	if req.Name != "prod-abc" || req.Region != "nyc3" || req.Size != "s-2vcpu-4gb" || req.Image.Slug != "debian-12-x64" {
		t.Fatalf("bad request: %#v", req)
	}
	if len(req.SSHKeys) != 1 || req.SSHKeys[0].ID != 77 {
		t.Fatalf("SSHKeys = %#v", req.SSHKeys)
	}
	if req.UserData != "#cloud-config" {
		t.Fatalf("UserData = %q", req.UserData)
	}
	if inst.ID != "101" || inst.IPv4 != "203.0.113.10" || !inst.CreatedAt.Equal(created) {
		t.Fatalf("instance = %+v", inst)
	}
}
```

- [ ] **Step 2: Run test and verify it fails**

Run:

```sh
go test ./internal/provider/digitalocean -run TestProvisionCreatesDroplet -count=1 -v
```

Expected: FAIL because `Provision` is not implemented.

- [ ] **Step 3: Implement provisioning**

Create `internal/provider/digitalocean/provision.go`:

```go
package digitalocean

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/digitalocean/godo"

	"github.com/hstern/fj-bellows/internal/provider"
)

func (d *DigitalOcean) Provision(ctx context.Context, spec provider.Spec) (provider.Instance, error) {
	keyID, err := d.ensureSSHKey(ctx, spec.AuthorizedKey)
	if err != nil {
		return provider.Instance{}, err
	}
	if err := d.ensureFirewall(ctx); err != nil {
		return provider.Instance{}, err
	}
	droplet, err := d.client.CreateDroplet(ctx, &godo.DropletCreateRequest{
		Name:   spec.Name,
		Region: d.cfg.Region,
		Size:   d.cfg.Size,
		Image: godo.DropletCreateImage{
			Slug: d.cfg.Image,
		},
		SSHKeys: []godo.DropletCreateSSHKey{{ID: keyID}},
		UserData: spec.UserData,
		Tags:     []string{spec.Tag},
	})
	if err != nil {
		return provider.Instance{}, fmt.Errorf("digitalocean: create droplet: %w", err)
	}
	droplet, err = d.pollDropletPublicIP(ctx, droplet.ID)
	if err != nil {
		return provider.Instance{}, err
	}
	return toInstance(*droplet), nil
}

func (d *DigitalOcean) pollDropletPublicIP(ctx context.Context, id int) (*godo.Droplet, error) {
	interval := d.pollInterval
	if interval == 0 {
		interval = 2 * time.Second
	}
	for {
		d, err := d.client.GetDroplet(ctx, id)
		if err == nil && publicIPv4(*d) != "" {
			return d, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("digitalocean: wait for droplet public ip: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

func toInstance(d godo.Droplet) provider.Instance {
	created, _ := time.Parse(time.RFC3339, d.Created)
	return provider.Instance{ID: strconv.Itoa(d.ID), Name: d.Name, IPv4: publicIPv4(d), CreatedAt: created, Tag: firstTag(d.Tags)}
}

func publicIPv4(d godo.Droplet) string {
	if d.Networks == nil {
		return ""
	}
	for _, n := range d.Networks.V4 {
		if n.Type == "public" {
			return n.IPAddress
		}
	}
	return ""
}

func firstTag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	return tags[0]
}
```

Modify `DigitalOcean` in `digitalocean.go` to add:

```go
	pollInterval time.Duration
```

Add `time` import to `digitalocean.go` only if needed by the compiler; otherwise keep it in `provision.go`.

- [ ] **Step 4: Run provision tests**

Run:

```sh
go test ./internal/provider/digitalocean -run TestProvisionCreatesDroplet -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/provider/digitalocean
git commit -m "feat(digitalocean): provision droplets"
```

---

### Task 6: Implement List and Destroy

**Files:**
- Create: `internal/provider/digitalocean/list.go`
- Create: `internal/provider/digitalocean/destroy.go`
- Test: `internal/provider/digitalocean/list_test.go`
- Test: `internal/provider/digitalocean/destroy_test.go`

- [ ] **Step 1: Write failing list and destroy tests**

Create `internal/provider/digitalocean/list_test.go`:

```go
package digitalocean

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

func TestListFiltersByTagAndSkipsDropletsWithoutPublicIP(t *testing.T) {
	f := &fakeClient{droplets: []godo.Droplet{
		{ID: 1, Name: "ready", Tags: []string{"prod"}, Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.10"}}}},
		{ID: 2, Name: "booting", Tags: []string{"prod"}},
		{ID: 3, Name: "other", Tags: []string{"other"}, Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.11"}}}},
	}}
	d := &DigitalOcean{client: f}
	insts, err := d.List(context.Background(), "prod")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(insts) != 1 || insts[0].ID != "1" || insts[0].IPv4 != "203.0.113.10" {
		t.Fatalf("instances = %+v", insts)
	}
}
```

Create `internal/provider/digitalocean/destroy_test.go`:

```go
package digitalocean

import (
	"context"
	"testing"
)

func TestDestroyDeletesDroplet(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{client: f}
	if err := d.Destroy(context.Background(), "123"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(f.deletedDroplets) != 1 || f.deletedDroplets[0] != 123 {
		t.Fatalf("deletedDroplets = %+v", f.deletedDroplets)
	}
}
```

- [ ] **Step 2: Run tests and verify they fail**

Run:

```sh
go test ./internal/provider/digitalocean -run 'TestList|TestDestroy' -count=1 -v
```

Expected: FAIL because `List` and `Destroy` are not implemented.

- [ ] **Step 3: Implement list and destroy**

Create `internal/provider/digitalocean/list.go`:

```go
package digitalocean

import (
	"context"
	"fmt"

	"github.com/hstern/fj-bellows/internal/provider"
)

func (d *DigitalOcean) List(ctx context.Context, tag string) ([]provider.Instance, error) {
	droplets, err := d.client.ListDropletsByTag(ctx, tag)
	if err != nil {
		return nil, fmt.Errorf("digitalocean: list droplets: %w", err)
	}
	var out []provider.Instance
	for _, droplet := range droplets {
		if publicIPv4(droplet) == "" {
			continue
		}
		out = append(out, toInstance(droplet))
	}
	return out, nil
}
```

Create `internal/provider/digitalocean/destroy.go`:

```go
package digitalocean

import (
	"context"
	"fmt"
	"strconv"
)

func (d *DigitalOcean) Destroy(ctx context.Context, id string) error {
	n, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("digitalocean: invalid droplet id %q: %w", id, err)
	}
	if err := d.client.DeleteDroplet(ctx, n); err != nil {
		return fmt.Errorf("digitalocean: delete droplet %s: %w", id, err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run:

```sh
go test ./internal/provider/digitalocean -run 'TestList|TestDestroy' -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add internal/provider/digitalocean
git commit -m "feat(digitalocean): list and destroy droplets"
```

---

### Task 7: Wire Provider Into Binary and Examples

**Files:**
- Modify: `cmd/fj-bellows/main.go`
- Modify: `config.example.yaml`
- Modify: `internal/provider/digitalocean/README.md`

- [ ] **Step 1: Write integration expectation**

Run before changes:

```sh
go test ./cmd/fj-bellows ./internal/provider/digitalocean -count=1
```

Expected: PASS for current package tests. This is a baseline, not a red test.

- [ ] **Step 2: Add blank import**

In `cmd/fj-bellows/main.go`, add:

```go
	_ "github.com/hstern/fj-bellows/internal/provider/digitalocean"
```

near the other provider blank imports.

- [ ] **Step 3: Add config example**

In `config.example.yaml`, add a commented example block:

```yaml
# provider: digitalocean
# provider_config:
#   token: ${DIGITALOCEAN_TOKEN}
#   region: nyc3
#   size: s-2vcpu-4gb
#   image: debian-12-x64
#   firewall:
#     allow_inbound:
#       - auto
#     refresh_interval: 1h
# poll:
#   idle_timeout: 1s
```

- [ ] **Step 4: Expand README**

Append to `internal/provider/digitalocean/README.md`:

```markdown
## Token scope

The DigitalOcean token needs read/write access for Droplets, SSH keys, tags, and
firewalls.

## Billing

DigitalOcean Droplets are treated as per-second billed. Use a low
`poll.idle_timeout`, for example `1s`, if every job should get a fresh Droplet.

## Managed firewall

The managed firewall allows inbound tcp/22 from `allow_inbound` and permits all
outbound traffic. `auto` resolves the orchestrator host's public IPs at startup.
```

- [ ] **Step 5: Run tests**

Run:

```sh
go test ./cmd/fj-bellows ./internal/provider/digitalocean -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add cmd/fj-bellows/main.go config.example.yaml internal/provider/digitalocean/README.md
git commit -m "feat(digitalocean): wire provider into binary"
```

---

### Task 8: Deployment Config Changes

**Files:**
- Modify outside this repo if using the `sauce` deployment repo: `../sauce/fj-bellows/config.yaml`
- Modify SOPS secret outside this repo: `../sauce/fj-bellows/secrets/secrets.yaml`

- [ ] **Step 1: Update config map**

Change the provider block to:

```yaml
provider: digitalocean
provider_config:
  token: ${DIGITALOCEAN_TOKEN}
  region: nyc3
  size: s-2vcpu-4gb
  image: debian-12-x64
  firewall:
    allow_inbound:
      - auto
    refresh_interval: 1h
poll:
  interval: 5s
  idle_timeout: 1s
```

- [ ] **Step 2: Update secret environment**

In the deployment secret, replace or add the DigitalOcean token key. The final
environment mounted into the fj-bellows pod must provide:

```sh
DIGITALOCEAN_TOKEN=<token>
FORGEJO_TOKEN=<token>
```

Keep `RUNPOD_API_KEY` only if another deployment still uses Runpod.

- [ ] **Step 3: Commit deployment repo separately**

Run from the deployment repo root:

```sh
git status --short
git diff
git add fj-bellows/config.yaml fj-bellows/secrets/secrets.yaml
git commit -m "feat(fj-bellows): switch workers to digitalocean"
```

Expected: only intended deployment files are staged.

---

### Task 9: Full Verification

**Files:**
- No code changes unless verification finds a bug.

- [ ] **Step 1: Run package tests**

```sh
go test ./internal/provider/digitalocean -count=1 -v
```

Expected: PASS.

- [ ] **Step 2: Run full race tests**

```sh
go test -race ./...
```

Expected: PASS.

- [ ] **Step 3: Run build**

```sh
go build ./...
```

Expected: no output and exit 0.

- [ ] **Step 4: Run lint if available**

```sh
make lint
```

Expected: PASS. If `golangci-lint` is not installed, report that explicitly.

- [ ] **Step 5: Commit any verification fixes**

If verification required fixes:

```sh
git add <fixed-files>
git commit -m "fix(digitalocean): address verification failures"
```

If no fixes were needed, do not create an empty commit.

---

## Self-Review Notes

- Spec coverage: provider name, godo SDK, Droplets, per-second billing, managed firewall, SSH dispatcher reuse, package README, config example, hermetic tests, and deployment env changes are covered.
- Scope: VPC, cache, placement, snapshots, and non-SSH dispatch are omitted as required.
- Ambiguity: firewall targets use tags in v1 because `godo.FirewallRequest` supports `Tags`; if DigitalOcean rejects tag-based targets in practice, adjust `firewallRequest` to use Droplet IDs and add a test in Task 4 before implementation.
