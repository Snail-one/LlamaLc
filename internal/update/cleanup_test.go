package update

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
)

func TestCleanupClassifiesLegacyAndSafeUpdaterSeparately(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	legacy := filepath.Join(root, "data", "llama.cpp")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep.gguf"), []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.Bin, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(l.Bin, ".llamaup-run-0123456789abcdef")
	if err := os.WriteFile(runner, []byte("runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	items, err := CleanupCandidates(l)
	if err != nil {
		t.Fatal(err)
	}
	var legacyItem, runnerItem *CleanupCandidate
	for i := range items {
		switch items[i].Path {
		case legacy:
			legacyItem = &items[i]
		case runner:
			runnerItem = &items[i]
		}
	}
	if legacyItem == nil || legacyItem.Automatic || !legacyItem.Legacy {
		t.Fatalf("legacy=%+v", legacyItem)
	}
	if runnerItem == nil || !runnerItem.Automatic || runnerItem.Recent {
		t.Fatalf("runner=%+v", runnerItem)
	}
	if err := os.WriteFile(filepath.Join(legacy, "changed"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DeleteCandidate(l, *legacyItem); err == nil {
		t.Fatal("changed legacy directory was deleted")
	}
}
