package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/digitalocean/godo"
)

type fakeClient struct {
	mu sync.Mutex

	droplets []godo.Droplet
	keys     []godo.Key
	firewall []godo.Firewall
	fwSeq    int

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
	return nil, errors.New("not found")
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

func (f *fakeClient) ListKeys(context.Context) ([]godo.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys, nil
}

func (f *fakeClient) CreateKey(_ context.Context, req *godo.KeyCreateRequest) (*godo.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createKeyReqs = append(f.createKeyReqs, req)
	k := godo.Key{ID: 500 + len(f.createKeyReqs), Name: req.Name, PublicKey: req.PublicKey}
	f.keys = append(f.keys, k)
	return &k, nil
}

func (f *fakeClient) ListFirewalls(context.Context) ([]godo.Firewall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.firewall, nil
}

func (f *fakeClient) CreateFirewall(_ context.Context, req *godo.FirewallRequest) (*godo.Firewall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createFWReqs = append(f.createFWReqs, req)
	f.fwSeq++
	fw := godo.Firewall{ID: fmt.Sprintf("fw-%d", f.fwSeq), Name: req.Name, Tags: req.Tags}
	f.firewall = append(f.firewall, fw)
	return &fw, nil
}

func (f *fakeClient) UpdateFirewall(_ context.Context, id string, req *godo.FirewallRequest) (*godo.Firewall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateFWReqs = append(f.updateFWReqs, req)
	return &godo.Firewall{ID: id, Name: req.Name, Tags: req.Tags}, nil
}

func (f *fakeClient) DeleteFirewall(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedFirewalls = append(f.deletedFirewalls, id)
	return nil
}

func (f *fakeClient) CreateTag(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return nil
}
