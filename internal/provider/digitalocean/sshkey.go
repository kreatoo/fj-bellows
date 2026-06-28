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
