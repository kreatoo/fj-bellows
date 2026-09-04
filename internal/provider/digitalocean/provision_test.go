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

func TestProvisionCleansUpWhenContextCancelledAfterCreate(t *testing.T) {
	f := &fakeClient{keys: []godo.Key{{ID: 77, PublicKey: testAuthorizedKey}}}
	d := &DigitalOcean{
		tag:          "prod",
		client:       f,
		cfg:          config{Region: "nyc3", Size: "s-2vcpu-4gb", Image: "debian-12-x64", Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}},
		pollInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Provision(ctx, provider.Spec{Tag: "prod", Name: "prod-failed", AuthorizedKey: testAuthorizedKey}); err == nil {
		t.Fatal("Provision unexpectedly succeeded")
	}
	if len(f.deletedDroplets) != 1 || f.deletedDroplets[0] != 101 {
		t.Fatalf("deletedDroplets = %v, want [101]", f.deletedDroplets)
	}
}

func TestProvisionUsesLabelMapping(t *testing.T) {
	f := &fakeClient{keys: []godo.Key{{ID: 77, PublicKey: testAuthorizedKey}}}
	created := time.Now().UTC().Format(time.RFC3339)
	f.droplets = []godo.Droplet{{ID: 101, Created: created, Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.10"}}}}}
	d := &DigitalOcean{tag: "prod", client: f, cfg: config{Region: "nyc3", Labels: map[string]labelConfig{"ubuntu-latest": {Size: "s-4vcpu-8gb", Image: "12345"}}, Firewall: firewallConfig{AllowInbound: []string{"203.0.113.5/32"}}}, pollInterval: time.Millisecond}
	if _, err := d.Provision(context.Background(), provider.Spec{Tag: "prod", Labels: []string{"ubuntu-latest:docker://example/image"}, AuthorizedKey: testAuthorizedKey}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	req := f.createDropletReqs[0]
	if req.Size != "s-4vcpu-8gb" || req.Image.ID != 12345 || req.Image.Slug != "" {
		t.Fatalf("request image/size = %#v / %q", req.Image, req.Size)
	}
}

func TestImageRef(t *testing.T) {
	if got := imageRef("123"); got.ID != 123 || got.Slug != "" {
		t.Fatalf("numeric image = %#v", got)
	}
	if got := imageRef("debian-12-x64"); got.Slug != "debian-12-x64" || got.ID != 0 {
		t.Fatalf("slug image = %#v", got)
	}
}
