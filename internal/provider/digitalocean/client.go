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
