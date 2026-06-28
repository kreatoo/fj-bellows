package digitalocean

import (
	"context"
	"testing"
)

func TestDestroyInvalidID(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{client: f}
	if err := d.Destroy(context.Background(), "not-a-number"); err == nil {
		t.Fatal("expected error for invalid id")
	}
}

func TestDestroyDeletesDroplet(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{client: f}
	if err := d.Destroy(context.Background(), "123"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(f.deletedDroplets) != 1 || f.deletedDroplets[0] != 123 {
		t.Fatalf("deletedDroplets = %+v", f.deletedDroplets)
	}
}
