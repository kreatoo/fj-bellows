package digitalocean

import "github.com/digitalocean/godo"

type doClient interface{}

type godoClient struct {
	client *godo.Client
}

type fakeClient struct{}
