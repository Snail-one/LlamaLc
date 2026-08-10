package procinfo

import (
	"os"
	"testing"
)

func TestIdentityDistinguishesLiveAndMissingProcess(t *testing.T) {
	identity, alive, err := Identity(os.Getpid())
	if err != nil || !alive || identity == "" {
		t.Fatalf("current process identity=%q alive=%v err=%v", identity, alive, err)
	}
	again, alive, err := Identity(os.Getpid())
	if err != nil || !alive || again != identity {
		t.Fatalf("identity changed: first=%q second=%q alive=%v err=%v", identity, again, alive, err)
	}
	if _, alive, err := Identity(2_147_483_647); err != nil || alive {
		t.Fatalf("missing process alive=%v err=%v", alive, err)
	}
}
