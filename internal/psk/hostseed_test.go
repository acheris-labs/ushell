package psk

import (
	"encoding/hex"
	"testing"
)

// Computed independently with Python's hmac/hashlib (RFC 5869, salt = zeros)
// for the RFC 8032 test seed.
func TestHostSeedMatchesIndependentHKDF(t *testing.T) {
	seed, _ := hex.DecodeString(rfcSeed)
	for purpose, want := range map[string]string{
		"sshd":    "c2659c006b72a134efe6f2dcc695f283301784d74e52b219d4f0884433f22a0d",
		"uptermd": "1f078e415cf5dd7bdc06280d8ab6d2d20e8ad5076dc7491b0c30e4dc35a81c1b",
	} {
		if got := hex.EncodeToString(hostSeed(seed, purpose)); got != want {
			t.Errorf("%s: %s, want %s", purpose, got, want)
		}
	}
}
