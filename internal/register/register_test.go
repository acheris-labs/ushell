package register

import (
	"strings"
	"testing"
)

func TestRejectionNamesAnOldRendezvous(t *testing.T) {
	cases := map[string]string{
		"ush: unsupported command: share\r\n": "older than this ush",
		"ush: share: bad name\r\n":            "rendezvous rejected registration: ush: share: bad name",
		"":                                    "no acknowledgement",
	}
	for stderr, want := range cases {
		if got := rejection(stderr).Error(); !strings.Contains(got, want) {
			t.Errorf("rejection(%q) = %q, want it to mention %q", stderr, got, want)
		}
	}
}
