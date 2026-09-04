# internal/provider/digitalocean

DigitalOcean implementation of `provider.Provider`, built on the official
`godo` SDK.

`provider_config` shape:

```yaml
provider_config:
  token: ${DIGITALOCEAN_TOKEN}
  region: nyc3
  size: s-2vcpu-4gb
  image: debian-12-x64
  firewall:
    allow_inbound:
      - auto
    refresh_interval: 1h
```

DigitalOcean is treated as per-second billed; use low `poll.idle_timeout` for
one-job-per-Droplet behavior. The legacy `ssh` transport is required; DO
workers do not have a VPC address and cannot use `cache-gateway` mode.

## Token scope

The DigitalOcean token needs read/write access for Droplets, SSH keys, tags, and
firewalls.

## Billing

DigitalOcean Droplets are treated as per-second billed. Use a low
`poll.idle_timeout`, for example `1s`, if every job should get a fresh Droplet.

## Managed firewall

The managed firewall allows inbound tcp/22 from `allow_inbound` and permits all
outbound traffic. `auto` resolves the orchestrator host's public IPs at startup.


### Per-label worker shapes

When a pool serves multiple runner classes, `labels` may map a bare Forgejo
label to a Droplet size/image. A label-specific value overrides the global
value; numeric image values are treated as DigitalOcean image IDs.

```yaml
provider_config:
  labels:
    ubuntu-latest:
      size: s-2vcpu-4gb
      image: debian-12-x64
```

The provider refreshes managed firewall rules at `refresh_interval`, retains
the last known-good ingress list if public-IP discovery temporarily fails, and
recreates a firewall deleted outside fj-bellows.
