# DigitalOcean Provider Design

## Goal

Add a `digitalocean` provider for fj-bellows that provisions real DigitalOcean
Droplets as ephemeral Forgejo Actions workers. This replaces the Runpod CPU pod
experiment, which cannot support nested Docker because Runpod containers lack
the privileges Docker needs to apply image layers.

## Requirements

- Use the official `godo` SDK instead of a hand-written REST client.
- Use DigitalOcean Droplets, not App Platform or Kubernetes.
- Treat DigitalOcean as per-second billed and use aggressive idle teardown.
- Include managed firewall support in v1.
- Reuse the existing orchestrator cloud-init bootstrap and SSH dispatcher.
- Keep provider-specific config inside `provider_config`.
- Keep tests hermetic with hand-written mocks; no real DigitalOcean calls.

## Non-Goals

- No VPC/private networking in v1.
- No managed registry/cache integration in v1.
- No placement/anti-affinity equivalent in v1.
- No snapshots/images managed by fj-bellows in v1.
- No support for non-SSH dispatch modes in v1.

## Configuration

Example:

```yaml
provider: digitalocean
provider_config:
  token: ${DIGITALOCEAN_TOKEN}
  region: nyc3
  size: s-2vcpu-4gb
  image: debian-12-x64
  firewall:
    allow_inbound:
      - auto
    refresh_interval: 1h
poll:
  idle_timeout: 1s
```

Fields:

- `token`: DigitalOcean API token. Required.
- `region`: Droplet region slug. Required.
- `size`: Droplet size slug. Required.
- `image`: Droplet image slug or ID. Required.
- `firewall`: managed firewall config. Required for v1 unless we later add an
  explicit `firewall_id` mode.
- `firewall.allow_inbound`: CIDRs plus `auto`, using the same sentinel behavior
  as the Linode provider.
- `firewall.refresh_interval`: optional, default `1h`, minimum `1m`.

## Provider Lifecycle

### Configure

`Configure(ctx, tag, node)` decodes `provider_config`, initializes a `godo`
client, stores `tag`, resolves `firewall.allow_inbound`, and ensures the managed
firewall exists.

The managed firewall is named from `cfg.Tag`, for example
`fj-bellows-<sanitized-tag>`, with truncation if needed. The provider uses tags
for ownership and lookup where DigitalOcean supports them.

If `allow_inbound` contains `auto`, failure to resolve local public IPs is fatal
at Configure. Runtime refresh failures keep the previous-known-good firewall
rules.

### Provision

`Provision(ctx, spec)` creates a Droplet with:

- name: `spec.Name`
- region: configured region
- size: configured size
- image: configured image
- tags: at least `spec.Tag`
- user data: `spec.UserData`
- SSH keys: an imported/reused key corresponding to `spec.AuthorizedKey`

The provider imports the orchestrator SSH public key once per deployment if it
does not already exist. The key name should be deterministic from `cfg.Tag` so a
restart reuses it instead of creating duplicates.

After Droplet creation, the provider attaches or updates the managed firewall so
it applies to the deployment's Droplets. `Provision` polls until the Droplet has
a public IPv4 address and returns `provider.Instance` with ID, name, public IPv4,
CreatedAt, and tag.

### List

`List(ctx, tag)` lists Droplets by tag and returns all matching instances. It is
the source of truth for crash recovery and orphan sweep. Droplets without a
public IPv4 are skipped until they become reachable.

### Destroy

`Destroy(ctx, id)` deletes the Droplet by ID. After successful deletion, the
provider checks whether any tagged Droplets remain. If none remain, it may reap
the managed firewall and clear cached IDs so the next `Provision` recreates it.

### Billing Model

`BillingModel()` returns `provider.BillingPerSecond`. Operators should configure
a low `poll.idle_timeout` such as `1s` when they want one-job-per-VM behavior.

## Managed Firewall

The v1 managed firewall allows:

- inbound tcp/22 from resolved `allow_inbound` CIDRs
- outbound all traffic, so workers can reach Forgejo through the dispatch tunnel,
  package mirrors, Docker registries, and language package registries

The firewall should target Droplets by tag if DigitalOcean supports tag-based
firewall rules through `godo`; otherwise it should attach Droplet IDs at
provision time and refresh attachments during reconcile-related operations.

Firewall refresh follows Linode's model:

- fatal at Configure if initial sentinel resolution fails
- background refresh at `refresh_interval`
- runtime refresh failure is logged and leaves existing rules intact

## Interfaces and Tests

The provider package should define narrow client interfaces around the `godo`
methods it uses, for example:

- Droplet create/get/list/delete
- SSH key list/create
- Firewall create/list/update/delete

Tests use hand-written fakes implementing those interfaces. They should cover:

- required config validation
- billing model is per-second
- SSH key import/reuse
- Droplet create request contents
- poll until public IPv4
- list filters by tag and skips no-IP Droplets
- destroy calls Droplet delete
- managed firewall create/update/reap behavior
- `auto` sentinel failure is fatal at Configure

Every new package must include `README.md` per repository convention.

## Integration Points

- Register provider as `digitalocean` in `internal/provider/digitalocean`.
- Blank-import it in `cmd/fj-bellows/main.go`.
- Add `config.example.yaml` example provider config.
- Add `internal/provider/digitalocean/README.md` documenting config, token
  scopes, billing behavior, and firewall behavior.

## DigitalOcean Token Scope

The token needs read/write access for Droplets, SSH keys, tags, and firewalls.
The README should document the narrowest DigitalOcean token scopes available at
implementation time.

## Open Questions

- Whether DigitalOcean firewall rules can target tags via the `godo` surface in
  the version we use. If not, v1 will attach by Droplet ID and refresh the
  firewall target list after create/delete.
- Whether we should support `firewall_id` as a follow-up for operators who want
  to manage firewall policy outside fj-bellows.
