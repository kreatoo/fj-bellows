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
