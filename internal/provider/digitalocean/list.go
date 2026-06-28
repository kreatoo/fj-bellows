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
