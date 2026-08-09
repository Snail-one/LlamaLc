//go:build windows

package launcher

import (
	"os"
	"testing"
)

func TestSignalUpdateReadyRejectsUntrustedEventName(t *testing.T) {
	t.Setenv("LLAMALC_UPDATE_READY_EVENT", `Global\untrusted`)
	if err := signalUpdateReady(); err == nil {
		t.Fatal("accepted untrusted event name")
	}
	if _, exists := os.LookupEnv("LLAMALC_UPDATE_READY_EVENT"); exists {
		t.Fatal("event environment was not cleared")
	}
}
