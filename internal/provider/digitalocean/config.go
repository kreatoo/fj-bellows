package digitalocean

import (
	"errors"
	"time"
)

type config struct {
	Token    string                `yaml:"token"`
	Region   string                `yaml:"region"`
	Size     string                `yaml:"size"`
	Image    string                `yaml:"image"`
	Firewall firewallConfig        `yaml:"firewall"`
	Labels   map[string]labelConfig `yaml:"labels"`
}

type labelConfig struct {
	Image string `yaml:"image"`
	Size  string `yaml:"size"`
}

type firewallConfig struct {
	AllowInbound    []string      `yaml:"allow_inbound"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

func (c *config) setDefaults() {
	if c.Firewall.RefreshInterval == 0 {
		c.Firewall.RefreshInterval = time.Hour
	}
}

func (c config) validate() error {
	if c.Token == "" {
		return errors.New("digitalocean: provider_config missing: token")
	}
	if c.Region == "" {
		return errors.New("digitalocean: provider_config missing: region")
	}
	if len(c.Labels) == 0 {
		if c.Size == "" {
			return errors.New("digitalocean: provider_config missing: size (or use labels map)")
		}
		if c.Image == "" {
			return errors.New("digitalocean: provider_config missing: image (or use labels map)")
		}
	}
	if len(c.Firewall.AllowInbound) == 0 {
		return errors.New("digitalocean: provider_config missing: firewall.allow_inbound")
	}
	if c.Firewall.RefreshInterval < time.Minute {
		return errors.New("digitalocean: firewall.refresh_interval must be >= 1m")
	}
	return nil
}
