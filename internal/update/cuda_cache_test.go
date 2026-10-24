package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
)

type cudaCacheSource struct {
	release release.GitHubRelease
	data    []byte
	err     error
	calls   int
}

func (s *cudaCacheSource) Latest(context.Context, string) (release.GitHubRelease, error) {
	return s.release, nil
}

func (s *cudaCacheSource) Download(_ context.Context, _ release.Asset, path string) error {
	s.calls++
	if err := os.WriteFile(path, s.data, 0o600); err != nil {
		return err
	}
	return s.err
}

func newCUDACacheManager(t *testing.T) (*Manager, *cudaCacheSource, release.Asset) {
	t.Helper()
	l, err := layout.New(filepath.Join(t.TempDir(), "LlamaLc"), "windows")
	if err != nil {
		t.Fatal(err)
	}
	source := &cudaCacheSource{data: []byte("CUDA runtime bytes")}
	m := NewManager(l, source)
	m.GOOS, m.GOARCH = "windows", "amd64"
	return m, source, fixtureAsset("cudart-llama-bin-win-cuda-13.4-x64.zip", source.data)
}

func cacheTestStaging(t *testing.T, m *Manager) string {
	t.Helper()
	if err := os.MkdirAll(m.Layout.LlamaRuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := os.MkdirTemp(m.Layout.LlamaRuntimeDir, ".test-staging-")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCUDACacheReuseAcrossVersionsAndManagerRestarts(t *testing.T) {
	m, source, asset := newCUDACacheManager(t)
	staging := cacheTestStaging(t, m)
	path, err := m.cudaRuntimeArchive(context.Background(), asset, staging)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	// A different release URL with the same digest must reuse the same bytes.
	asset.URL = "https://example.invalid/releases/download/b999/" + asset.Name
	source.err = errors.New("network must not be needed")
	restarted := NewManager(m.Layout, source)
	restarted.GOOS, restarted.GOARCH = "windows", "amd64"
	second, err := restarted.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, restarted))
	if err != nil || second != path || source.calls != 1 {
		t.Fatalf("path=%q err=%v downloads=%d", second, err, source.calls)
	}
	main := fixtureAsset("llama-b999-bin-win-cuda-13.4-x64.zip", []byte("runtime"))
	source.release = release.GitHubRelease{Tag: "b999", Assets: []release.Asset{asset, main}}
	// Remove the test staging directory so preflight sees an empty runtime tree.
	if err := os.RemoveAll(m.Layout.LlamaRuntimeDir); err != nil {
		t.Fatal(err)
	}
	plan, err := restarted.PrepareLlama(context.Background(), LlamaOptions{Backend: "cuda-13.4"})
	if err != nil || plan.DownloadSize != main.Size || plan.CachedSize != asset.Size || plan.NeedsRecovery {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestCUDACacheChangedDigestDownloadsNewArchive(t *testing.T) {
	m, source, asset := newCUDACacheManager(t)
	first, err := m.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, m))
	if err != nil {
		t.Fatal(err)
	}
	source.data = []byte("new CUDA runtime bytes")
	changed := fixtureAsset(asset.Name, source.data)
	second, err := m.cudaRuntimeArchive(context.Background(), changed, cacheTestStaging(t, m))
	if err != nil || first == second || source.calls != 2 {
		t.Fatalf("first=%q second=%q err=%v downloads=%d", first, second, err, source.calls)
	}
}

func TestCUDACacheCorruptionIsReplaced(t *testing.T) {
	for _, corrupt := range []string{"short", "CUDA runtime bytex"} {
		t.Run(corrupt, func(t *testing.T) {
			m, source, asset := newCUDACacheManager(t)
			path, err := m.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, m))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, m)); err != nil {
				t.Fatal(err)
			}
			valid, err := m.validCUDAArchive(path, asset)
			if err != nil || !valid || source.calls != 2 {
				t.Fatalf("valid=%v err=%v downloads=%d", valid, err, source.calls)
			}
		})
	}
}

func TestCUDACacheNeverPublishesFailedDownloads(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "bad-checksum", true: "interrupted"}[interrupted], func(t *testing.T) {
			m, source, asset := newCUDACacheManager(t)
			correct := append([]byte(nil), source.data...)
			source.data = []byte("CUDA runtime bytex")
			if interrupted {
				source.data = []byte("partial")
				source.err = errors.New("interrupted download")
			}
			if _, err := m.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, m)); err == nil {
				t.Fatal("accepted failed download")
			}
			path, _ := m.cudaCachePath(asset)
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("published failed download: %v", err)
			}
			source.data, source.err = correct, nil
			if _, err := m.cudaRuntimeArchive(context.Background(), asset, cacheTestStaging(t, m)); err != nil {
				t.Fatal(err)
			}
			if source.calls != 2 {
				t.Fatalf("downloads=%d", source.calls)
			}
		})
	}
}

func TestCUDACacheRejectsSymlinks(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			m, source, asset := newCUDACacheManager(t)
			path, _ := m.cudaCachePath(asset)
			staging := cacheTestStaging(t, m)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			link := path
			if directory {
				link = filepath.Dir(path)
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			} else {
				outside = filepath.Join(outside, "outside.zip")
				if err := os.WriteFile(outside, source.data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := m.cudaRuntimeArchive(context.Background(), asset, staging); err == nil || source.calls != 0 {
				t.Fatalf("err=%v downloads=%d", err, source.calls)
			}
		})
	}
}
