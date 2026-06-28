package digitalocean

import (
	"testing"

	"github.com/hstern/fj-bellows/internal/provider"
)

func TestRegistered(t *testing.T) {
	p, err := provider.New("digitalocean")
	if err != nil {
		t.Fatalf("provider.New: %v", err)
	}
	if p == nil {
		t.Fatal("provider.New returned nil")
	}
}

func TestBillingModelIsPerSecond(t *testing.T) {
	if got := (&DigitalOcean{}).BillingModel(); got != provider.BillingPerSecond {
		t.Fatalf("BillingModel = %v, want %v", got, provider.BillingPerSecond)
	}
}
