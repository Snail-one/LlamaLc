package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
	buildversion "github.com/Snail-one/LlamaLc/internal/version"
)

type fakeSource struct {
	release release.GitHubRelease
	files   map[string][]byte
}

func TestDamagedStateIsQuarantinedAndRepairCanRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "rollback"}[fail], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "LlamaLc")
			l, _ := layout.New(root, "linux")
			if err := os.MkdirAll(l.LlamaRuntimeDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(l.UpdateStateFile), 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(l.LlamaRuntimeDir, "old-runtime.txt")
			if err := os.WriteFile(marker, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(l.UpdateStateFile, []byte("{damaged"), 0o600); err != nil {
				t.Fatal(err)
			}
			archive := runtimeArchive(t, "b123")
			asset := release.Asset{Name: "llama-b123-bin-ubuntu-x64.tar.gz", Digest: "sha256:" + strings.Repeat("a", 64), Size: int64(len(archive))}
			rel := release.GitHubRelease{Tag: "b123", Assets: []release.Asset{asset}}
			files := map[string][]byte{asset.Name: archive}
			if fail {
				rel.Assets = nil
			}
			var output, errorOutput bytes.Buffer
			manager := NewManager(l, &fakeSource{release: rel, files: files})
			manager.GOOS, manager.GOARCH, manager.Out, manager.Err = "linux", "amd64", &output, &errorOutput
			_, err := manager.UpdateLlama(context.Background(), "cpu", true)
			if fail {
				if err == nil {
					t.Fatal("repair unexpectedly succeeded")
				}
				if data, readErr := os.ReadFile(l.UpdateStateFile); readErr != nil || string(data) != "{damaged" {
					t.Fatalf("state=%q err=%v", data, readErr)
				}
				if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "old" {
					t.Fatalf("marker=%q err=%v", data, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			entries, readErr := os.ReadDir(l.RecoveryDir)
			if readErr != nil || len(entries) != 1 {
				t.Fatalf("recovery entries=%v err=%v", entries, readErr)
			}
			recovery := filepath.Join(l.RecoveryDir, entries[0].Name())
			for _, path := range []string{filepath.Join(recovery, "update.json.corrupt"), filepath.Join(recovery, "llama.cpp", "old-runtime.txt"), filepath.Join(recovery, ".llamalc-recovery.json")} {
				if _, statErr := os.Stat(path); statErr != nil {
					t.Errorf("missing recovery file %s: %v", path, statErr)
				}
			}
			if !strings.Contains(output.String(), "恢复备份") || !strings.Contains(errorOutput.String(), "隔离") {
				t.Fatalf("out=%s err=%s", output.String(), errorOutput.String())
			}
		})
	}
}

func (f *fakeSource) Latest(context.Context, string) (release.GitHubRelease, error) {
	return f.release, nil
}
func (f *fakeSource) Download(_ context.Context, a release.Asset, path string) error {
	return os.WriteFile(path, f.files[a.Name], 0o600)
}
func runtimeArchive(t *testing.T, tag string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	script := []byte("#!/bin/sh\necho 'llama.cpp " + tag + "'\n")
	for _, name := range []string{"bundle/llama-server", "bundle/llama-cli"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(script); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func launcherArchive(t *testing.T, tag string, extra bool) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	script := []byte("#!/bin/sh\necho 'Version:   " + tag + "'\n")
	for _, name := range []string{"LlamaLc/bin/llamalc", "LlamaLc/bin/llamaup"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(script)
	}
	if extra {
		data := []byte("bad")
		_ = tw.WriteHeader(&tar.Header{Name: "LlamaLc/extra", Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buffer.Bytes()
}
func TestFirstInstallUpdateAndDowngradeRefusal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := runtimeArchive(t, "b123")
	asset := release.Asset{Name: "llama-b123-bin-ubuntu-x64.tar.gz", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: int64(len(archive))}
	source := &fakeSource{release: release.GitHubRelease{Tag: "b123", Assets: []release.Asset{asset}}, files: map[string][]byte{asset.Name: archive}}
	m := NewManager(l, source)
	m.GOOS = "linux"
	m.GOARCH = "amd64"
	tag, ids, current, err := m.AvailableLlamaBackends(context.Background())
	if err != nil || tag != "b123" || len(ids) != 1 || ids[0] != "cpu" || current != "" {
		t.Fatalf("catalog: tag=%q ids=%v current=%q err=%v", tag, ids, current, err)
	}
	if _, err := m.UpdateLlama(context.Background(), "", false); err == nil {
		t.Fatal("first install accepted empty backend")
	}
	state, err := m.UpdateLlama(context.Background(), "cpu", false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(RuntimePath(l, state)) != "b123" {
		t.Fatalf("runtime=%s", RuntimePath(l, state))
	}
	_, _, current, err = m.AvailableLlamaBackends(context.Background())
	if err != nil || current != "cpu" {
		t.Fatalf("current=%q err=%v", current, err)
	}
	if _, err = m.UpdateLlama(context.Background(), "", false); err == nil {
		t.Fatal("same version updated without reinstall")
	}
	source.release.Tag = "b124"
	source.release.Assets[0].Name = "llama-b124-bin-ubuntu-x64.tar.gz"
	source.files[source.release.Assets[0].Name] = runtimeArchive(t, "b124")
	source.release.Assets[0].Size = int64(len(source.files[source.release.Assets[0].Name]))
	state, err = m.UpdateLlama(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.PendingCleanup) != 1 {
		t.Fatalf("pending=%v", state.PendingCleanup)
	}
	source.release.Tag = "b122"
	source.release.Assets[0].Name = "llama-b122-bin-ubuntu-x64.tar.gz"
	source.files[source.release.Assets[0].Name] = runtimeArchive(t, "b122")
	source.release.Assets[0].Size = int64(len(source.files[source.release.Assets[0].Name]))
	if _, err = m.UpdateLlama(context.Background(), "", false); err == nil {
		t.Fatal("downgrade accepted")
	}
}

func TestPrepareLauncherStrictBundleAndSums(t *testing.T) {
	oldVersion := buildversion.Version
	buildversion.Version = "v1.0.9"
	t.Cleanup(func() { buildversion.Version = oldVersion })
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	launcherData := launcherArchive(t, "v1.1.0", false)
	sumsData := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  llamalc-linux-amd64-v1.1.0.tar.gz\n")
	asset := release.Asset{Name: "llamalc-linux-amd64-v1.1.0.tar.gz", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: int64(len(launcherData))}
	sums := release.Asset{Name: "SHA256SUMS.txt", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Size: int64(len(sumsData))}
	source := &fakeSource{release: release.GitHubRelease{Tag: "v1.1.0", Assets: []release.Asset{asset, sums}}, files: map[string][]byte{asset.Name: launcherData, sums.Name: sumsData}}
	m := NewManager(l, source)
	m.GOOS = "linux"
	m.GOARCH = "amd64"
	tag, launcher, updater, staging, err := m.PrepareLauncher(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(staging)
	if tag != "v1.1.0" {
		t.Fatal(tag)
	}
	if _, err = os.Stat(launcher); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(updater); err != nil {
		t.Fatal(err)
	}
	source.files[asset.Name] = launcherArchive(t, "v1.1.0", true)
	if _, _, _, badStage, err := m.PrepareLauncher(context.Background()); err == nil {
		os.RemoveAll(badStage)
		t.Fatal("accepted extra archive file")
	}
}
