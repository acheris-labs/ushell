package keep

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Whoever waits for a keeper polls Exists while it starts (ush share -k does,
// and the keeper's own share does). A poll must never cost the keeper its
// socket: one that does leaves the session running, unshared and unreachable.
func TestPollingAStartingKeeperDoesNotOrphanIt(t *testing.T) {
	withHome(t)
	for i := 0; i < 20; i++ { // unfixed, it failed on the first
		name := fmt.Sprintf("race%d", i)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = Run(name, []string{"sleep", "30"}, nil, os.Environ(), Size{Width: 80, Height: 24})
		}()
		deadline := time.Now().Add(3 * time.Second)
		for !Exists(name) {
			if time.Now().After(deadline) {
				sock, _ := SocketPath(name)
				_, statErr := os.Stat(sock)
				select {
				case <-done:
					t.Fatalf("attempt %d: the keeper exited", i)
				default:
				}
				t.Fatalf("attempt %d: the keeper is running but never answered; its socket: %v", i, statErr)
			}
		}
		if err := Stop(name); err != nil {
			t.Fatal(err)
		}
		<-done
	}
}
