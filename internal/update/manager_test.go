package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
)

type fakeSource struct {
	release release.GitHubRelease
	files   map[string][]byte
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
	asset := release.Asset{Name: "llama-b123-bin-ubuntu-x64.tar.gz", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	source := &fakeSource{release: release.GitHubRelease{Tag: "b123", Assets: []release.Asset{asset}}, files: map[string][]byte{asset.Name: runtimeArchive(t, "b123")}}
	m := NewManager(l, source)
	m.GOOS = "linux"
	m.GOARCH = "amd64"
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
	if _, err = m.UpdateLlama(context.Background(), "", false); err == nil {
		t.Fatal("same version updated without reinstall")
	}
	source.release.Tag = "b124"
	source.release.Assets[0].Name = "llama-b124-bin-ubuntu-x64.tar.gz"
	source.files[source.release.Assets[0].Name] = runtimeArchive(t, "b124")
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
	if _, err = m.UpdateLlama(context.Background(), "", false); err == nil {
		t.Fatal("downgrade accepted")
	}
}

func TestPrepareLauncherStrictBundleAndSums(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.RuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	asset := release.Asset{Name: "llamalc-linux-amd64-v1.1.0.tar.gz", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	sums := release.Asset{Name: "SHA256SUMS.txt", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	source := &fakeSource{release: release.GitHubRelease{Tag: "v1.1.0", Assets: []release.Asset{asset, sums}}, files: map[string][]byte{asset.Name: launcherArchive(t, "v1.1.0", false), sums.Name: []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  " + asset.Name + "\n")}}
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
