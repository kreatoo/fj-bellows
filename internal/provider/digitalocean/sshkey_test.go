package digitalocean

import (
	"context"
	"strings"
	"testing"

	"github.com/digitalocean/godo"
)

const testAuthorizedKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKey fj-bellows"

func TestEnsureSSHKeyReusesExistingPublicKey(t *testing.T) {
	f := &fakeClient{keys: []godo.Key{{ID: 77, Name: "other", PublicKey: testAuthorizedKey}}}
	d := &DigitalOcean{tag: "prod", client: f}
	id, err := d.ensureSSHKey(context.Background(), testAuthorizedKey)
	if err != nil {
		t.Fatalf("ensureSSHKey: %v", err)
	}
	if id != 77 {
		t.Fatalf("id = %d, want 77", id)
	}
	if len(f.createKeyReqs) != 0 {
		t.Fatalf("created key unexpectedly: %+v", f.createKeyReqs)
	}
}

func TestEnsureSSHKeyCreatesDeterministicName(t *testing.T) {
	f := &fakeClient{}
	d := &DigitalOcean{tag: "prod", client: f}
	id, err := d.ensureSSHKey(context.Background(), testAuthorizedKey)
	if err != nil {
		t.Fatalf("ensureSSHKey: %v", err)
	}
	if id == 0 {
		t.Fatal("id = 0")
	}
	if len(f.createKeyReqs) != 1 {
		t.Fatalf("CreateKey calls = %d, want 1", len(f.createKeyReqs))
	}
	if got := f.createKeyReqs[0].Name; got != "fj-bellows-prod" {
		t.Fatalf("Name = %q, want fj-bellows-prod", got)
	}
	if got := f.createKeyReqs[0].PublicKey; got != testAuthorizedKey {
		t.Fatalf("PublicKey = %q", got)
	}
}

func TestSanitizeName_Empty(t *testing.T) {
	if got := sanitizeName("", 63); got != "default" {
		t.Fatalf("sanitizeName('') = %q, want %q", got, "default")
	}
}

func TestSanitizeName_AllSpecialChars(t *testing.T) {
	if got := sanitizeName("!!!@@@###", 63); got != "default" {
		t.Fatalf("sanitizeName = %q, want %q", got, "default")
	}
}

func TestSanitizeName_AlreadyShort(t *testing.T) {
	if got := sanitizeName("my-tag-1", 63); got != "my-tag-1" {
		t.Fatalf("sanitizeName = %q, want %q", got, "my-tag-1")
	}
}

func TestSanitizeName_UpperCase(t *testing.T) {
	if got := sanitizeName("MyDeployment", 63); got != "mydeployment" {
		t.Fatalf("sanitizeName = %q, want %q", got, "mydeployment")
	}
}

func TestSanitizeName_HashTruncation(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := sanitizeName(long, 63)
	if len(got) > 63 {
		t.Fatalf("len = %d, want <= 63", len(got))
	}
	if !strings.Contains(got, "-") {
		t.Fatalf("hash-truncated name %q should contain a dash", got)
	}
}

func TestSanitizeName_SmallMax(t *testing.T) {
	// Should not panic even with tiny max.
	got := sanitizeName("hello-world", 5)
	if len(got) > 5 {
		t.Fatalf("len = %d, want <= 5", len(got))
	}
}
