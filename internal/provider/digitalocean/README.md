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
