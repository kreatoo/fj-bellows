package digitalocean

import (
	"context"
	"fmt"
	"log/slog"
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
		SSHKeys:  []godo.DropletCreateSSHKey{{ID: keyID}},
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
		droplet, err := d.client.GetDroplet(ctx, id)
		if err != nil {
			slog.Warn("digitalocean: poll droplet", "id", id, "err", err)
		} else if publicIPv4(*droplet) != "" {
			return droplet, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("digitalocean: wait for droplet public ip: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

func toInstance(d godo.Droplet) provider.Instance {
	created, err := time.Parse(time.RFC3339, d.Created)
	if err != nil {
		slog.Warn("digitalocean: parse droplet created time", "id", d.ID, "val", d.Created, "err", err)
		created = time.Now()
	}
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
