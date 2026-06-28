package digitalocean

import "testing"

func TestGodoClientSatisfiesInterfaces(t *testing.T) {
	var c any = &godoClient{}
	if _, ok := c.(dropletClient); !ok {
		t.Fatal("godoClient must satisfy dropletClient")
	}
	if _, ok := c.(keyClient); !ok {
		t.Fatal("godoClient must satisfy keyClient")
	}
	if _, ok := c.(firewallClient); !ok {
		t.Fatal("godoClient must satisfy firewallClient")
	}
	if _, ok := c.(tagClient); !ok {
		t.Fatal("godoClient must satisfy tagClient")
	}
}
