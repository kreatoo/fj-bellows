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
