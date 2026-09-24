package forgejo

import (
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestOneJobAcquisition(t *testing.T) {
	args := OneJobArgs("https://forgejo.example", "uuid", []string{"a", "b"}, "attempt")
	want := []string{
		"one-job", "--url", "https://forgejo.example", "--uuid", "uuid",
		"--token-url", "file:/tmp/tok", "--label", "a,b", "--handle", "attempt",
		"--config", "/tmp/runner-cfg.yml",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %q, want %q (no --wait or job-wide timeout)", args, want)
	}
	var cfg struct {
		Runner map[string]string `yaml:"runner"`
	}
	if err := yaml.Unmarshal([]byte(AcquisitionConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Runner) != 1 {
		t.Fatalf("must only change fetch_timeout, not task timeout: %v", cfg.Runner)
	}
	timeout, err := time.ParseDuration(cfg.Runner["fetch_timeout"])
	if err != nil || timeout != 2*time.Minute {
		t.Fatalf("fetch timeout = %v, err = %v", timeout, err)
	}
}
