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
one-job-per-Droplet behavior.

## Token scope

The DigitalOcean token needs read/write access for Droplets, SSH keys, tags, and
firewalls.

## Billing

DigitalOcean Droplets are treated as per-second billed. Use a low
`poll.idle_timeout`, for example `1s`, if every job should get a fresh Droplet.

## Managed firewall

The managed firewall allows inbound tcp/22 from `allow_inbound` and permits all
outbound traffic. `auto` resolves the orchestrator host's public IPs at startup.
