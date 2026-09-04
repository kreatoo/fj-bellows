package digitalocean

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/digitalocean/godo"
)

func (d *DigitalOcean) Destroy(ctx context.Context, id string) error {
	n, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("digitalocean: invalid droplet id %q: %w", id, err)
	}
	if err := d.client.DeleteDroplet(ctx, n); err != nil && !isNotFound(err) {
		return fmt.Errorf("digitalocean: delete droplet %s: %w", id, err)
	}
	return nil
}

// isNotFound makes teardown idempotent. A droplet deleted manually or by a
// previous retry is already in the desired state.
func isNotFound(err error) bool {
	var resp *godo.ErrorResponse
	return errors.As(err, &resp) && resp != nil && resp.Response != nil && resp.Response.StatusCode == http.StatusNotFound
}
