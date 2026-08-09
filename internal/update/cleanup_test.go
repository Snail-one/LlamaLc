package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
)

func TestCleanupIgnoresLegacyAndStrictlyRecognizesUpdaterRunner(t *testing.T) {
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
	var runnerItem *CleanupCandidate
	for i := range items {
		switch items[i].Path {
		case runner:
			runnerItem = &items[i]
		}
	}
	for _, item := range items {
		if item.Path == legacy {
			t.Fatalf("legacy path was inspected: %+v", item)
		}
	}
	if runnerItem == nil || !runnerItem.Automatic || runnerItem.Recent {
		t.Fatalf("runner=%+v", runnerItem)
	}
	invalid := filepath.Join(l.Bin, ".llamaup-run-not-a-token")
	if err := os.WriteFile(invalid, []byte("runner"), 0o700); err != nil {
		t.Fatal(err)
	}
	items, _ = CleanupCandidates(l)
	for _, item := range items {
		if item.Path == invalid {
			t.Fatal("accepted invalid runner name")
		}
	}
}

func TestCleanupDamagedStateStillListsRuntimeWithWarning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	runtimeDirectory := filepath.Join(l.LlamaRuntimeDir, "cpu", "b123")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.UpdateStateFile, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := CleanupCandidates(l)
	if err != nil {
		t.Fatal(err)
	}
	foundRuntime, foundWarning := false, false
	for _, item := range items {
		if item.Path == runtimeDirectory && strings.Contains(item.Reason, "扫描警告") {
			foundRuntime = true
		}
		if item.Warning && strings.Contains(item.Reason, "更新状态损坏") {
			foundWarning = true
		}
	}
	if !foundRuntime || !foundWarning {
		t.Fatalf("items=%+v", items)
	}
}

func TestCleanupRequiresValidOwnershipMarkerForTempDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	path := filepath.Join(l.LlamaRuntimeDir, ".staging-0123456789abcdef")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	items, _ := CleanupCandidates(l)
	for _, item := range items {
		if item.Path == path && item.Automatic {
			t.Fatal("unmarked directory accepted")
		}
	}
	if err := createOwnedTempDirectory(l, path+"2", "0123456789abcdef", "llama-runtime-staging"); err == nil {
		t.Fatal("accepted path whose name does not match token")
	}
}
