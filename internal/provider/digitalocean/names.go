package digitalocean

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func sanitizeName(s string, max int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "default"
	}
	if len(out) <= max {
		return out
	}
	sum := sha256.Sum256([]byte(out))
	suffix := hex.EncodeToString(sum[:])[:8]
	keep := max - len(suffix) - 1
	if keep <= 0 {
		return out[:max]
	}
	return strings.TrimRight(out[:keep], "-") + "-" + suffix
}
