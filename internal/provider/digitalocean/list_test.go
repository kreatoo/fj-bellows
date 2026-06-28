package digitalocean

import (
	"context"
	"testing"

	"github.com/digitalocean/godo"
)

func TestListFiltersByTagAndSkipsDropletsWithoutPublicIP(t *testing.T) {
	f := &fakeClient{droplets: []godo.Droplet{
		{ID: 1, Name: "ready", Tags: []string{"prod"}, Created: "2026-01-01T00:00:00Z", Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.10"}}}},
		{ID: 2, Name: "booting", Tags: []string{"prod"}, Created: "2026-01-01T00:00:00Z"},
		{ID: 3, Name: "other", Tags: []string{"other"}, Created: "2026-01-01T00:00:00Z", Networks: &godo.Networks{V4: []godo.NetworkV4{{Type: "public", IPAddress: "203.0.113.11"}}}},
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
