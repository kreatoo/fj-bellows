package digitalocean

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/digitalocean/godo"

	"github.com/hstern/fj-bellows/internal/provider"
)

const (
	// DigitalOcean normally assigns a public address within seconds. Keep a
	// finite upper bound so a control-plane/API regression cannot leave a
	// provision goroutine (and its droplet) hanging forever.
	publicIPWaitTimeout           = 5 * time.Minute
	failedProvisionDestroyTimeout = 1 * time.Minute
)

func (d *DigitalOcean) Provision(ctx context.Context, spec provider.Spec) (provider.Instance, error) {
	// Provision is called from the long-lived reconcile context. Bound the
	// entire cloud operation independently of that context.
	provisionCtx, cancel := context.WithTimeout(ctx, publicIPWaitTimeout)
	defer cancel()

	keyID, err := d.ensureSSHKey(provisionCtx, spec.AuthorizedKey)
	if err != nil {
		return provider.Instance{}, err
	}
	if err := d.ensureFirewall(provisionCtx); err != nil {
		return provider.Instance{}, err
	}
	size, image := d.resolveImageSizeForLabels(spec.Labels)
	if size == "" || image == "" {
		return provider.Instance{}, fmt.Errorf("digitalocean: no size/image mapping for labels %v", spec.Labels)
	}
	droplet, err := d.client.CreateDroplet(provisionCtx, &godo.DropletCreateRequest{
		Name:     spec.Name,
		Region:   d.cfg.Region,
		Size:     size,
		Image:    imageRef(image),
		SSHKeys:  []godo.DropletCreateSSHKey{{ID: keyID}},
		UserData: spec.UserData,
		Tags:     []string{spec.Tag},
	})
	if err != nil {
		// POST may have reached DigitalOcean even when the client observed an
		// error. Recover a same-name droplet before the next reconcile creates
		// another billable worker (names are unique per provision attempt).
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), failedProvisionDestroyTimeout)
		d.cleanupDropletByName(cleanupCtx, spec.Tag, spec.Name)
		cleanupCancel()
		return provider.Instance{}, fmt.Errorf("digitalocean: create droplet: %w", err)
	}
	if droplet == nil || droplet.ID == 0 {
		return provider.Instance{}, fmt.Errorf("digitalocean: create droplet returned no id")
	}
	dropletIDValue := droplet.ID
	droplet, err = d.pollDropletPublicIP(provisionCtx, dropletIDValue)
	if err != nil {
		// The API may have created the droplet even when address polling
		// failed. Best-effort cleanup prevents failed launches from leaking
		// billable droplets; preserve the original error for the caller.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), failedProvisionDestroyTimeout)
		cleanupErr := d.client.DeleteDroplet(cleanupCtx, dropletIDValue)
		cleanupCancel()
		if cleanupErr != nil && !isNotFound(cleanupErr) {
			return provider.Instance{}, fmt.Errorf("%w (cleanup droplet: %v)", err, cleanupErr)
		}
		return provider.Instance{}, err
	}
	return toInstance(*droplet), nil
}

func (d *DigitalOcean) cleanupDropletByName(ctx context.Context, tag, name string) {
	droplets, err := d.client.ListDropletsByTag(ctx, tag)
	if err != nil {
		slog.Warn("digitalocean: recover failed create", "name", name, "err", err)
		return
	}
	for _, candidate := range droplets {
		if candidate.Name != name || candidate.ID == 0 {
			continue
		}
		if err := d.client.DeleteDroplet(ctx, candidate.ID); err != nil && !isNotFound(err) {
			slog.Warn("digitalocean: cleanup failed create", "id", candidate.ID, "err", err)
		}
	}
}

func (d *DigitalOcean) pollDropletPublicIP(ctx context.Context, id int) (*godo.Droplet, error) {
	interval := d.pollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		droplet, err := d.client.GetDroplet(ctx, id)
		if err != nil {
			lastErr = err
			slog.Warn("digitalocean: poll droplet", "id", id, "err", err)
		} else if droplet == nil {
			lastErr = fmt.Errorf("empty droplet response")
		} else if publicIPv4(*droplet) != "" {
			return droplet, nil
		}
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return nil, fmt.Errorf("digitalocean: wait for droplet %d public ip: %w", id, lastErr)
		case <-ticker.C:
		}
	}
}

func imageRef(value string) godo.DropletCreateImage {
	if id, err := strconv.Atoi(value); err == nil && id > 0 {
		return godo.DropletCreateImage{ID: id}
	}
	return godo.DropletCreateImage{Slug: value}
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
		if n.Type == "public" && n.IPAddress != "" {
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

func (d *DigitalOcean) resolveImageSizeForLabels(labels []string) (size, image string) {
	// A worker spec carries the Forgejo labels, not the deployment tag. Use
	// the first configured mapping that matches. Configs commonly key this
	// map by the bare label while the spec may carry :docker://image.
	for _, label := range labels {
		keys := []string{label}
		if i := strings.IndexByte(label, ':'); i >= 0 {
			keys = append(keys, label[:i])
		}
		for _, key := range keys {
			if lc, ok := d.cfg.Labels[key]; ok {
				size, image = lc.Size, lc.Image
				if size == "" {
					size = d.cfg.Size
				}
				if image == "" {
					image = d.cfg.Image
				}
				return size, image
			}
		}
	}
	return d.cfg.Size, d.cfg.Image
}
