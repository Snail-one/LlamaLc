package updater

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
	"github.com/Snail-one/LlamaLc/internal/update"
)

type workflowSource struct {
	release   release.GitHubRelease
	err       error
	downloads int
	files     map[string][]byte
}

func (s *workflowSource) Latest(context.Context, string) (release.GitHubRelease, error) {
	return s.release, s.err
}
func (s *workflowSource) Download(_ context.Context, a release.Asset, path string) error {
	s.downloads++
	return os.WriteFile(path, s.files[a.Name], 0o700)
}

func TestCancelAndNoninteractiveUpdateDoNotDownloadOrReplace(t *testing.T) {
	for _, interactive := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "requires yes"}[interactive], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "LlamaLc")
			l, _ := layout.New(root, "linux")
			if err := os.MkdirAll(l.Bin, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{l.Launcher, l.Updater} {
				if err := os.WriteFile(p, []byte("original"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			source := &workflowSource{release: release.GitHubRelease{Tag: "v999.0.0", Assets: []release.Asset{
				{Name: "llamalc-linux-amd64-v999.0.0.tar.gz", Size: 123, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
				{Name: "SHA256SUMS.txt", Size: 123, Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			}}}
			manager := update.NewManager(l, source)
			manager.GOOS = "linux"
			manager.GOARCH = "amd64"
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			code := performUpdate(context.Background(), manager, update.LauncherOptions{}, false, interactive, bytes.NewBufferString("n\n"), stdout, stderr)
			want := 1
			if interactive {
				want = exitCancelled
			}
			if code != want {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			if source.downloads != 0 {
				t.Fatalf("downloads=%d", source.downloads)
			}
			assertProgram(t, l.Launcher, "original")
			assertProgram(t, l.Updater, "original")
		})
	}
}

func TestCheckLauncherSurvivesCorruptRuntimeState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	if err := os.MkdirAll(l.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.UpdateStateFile, []byte("broken state"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := update.NewManager(l, &workflowSource{release: release.GitHubRelease{Tag: "v999.0.0"}})
	if _, err := manager.Check(context.Background(), "launcher"); err != nil {
		t.Fatal(err)
	}
	assertProgram(t, l.UpdateStateFile, "broken state")
}

func TestFailedCheckIsNotReportedAsCurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LlamaLc")
	l, _ := layout.New(root, "linux")
	manager := update.NewManager(l, &workflowSource{err: errors.New("network unavailable")})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := performUpdate(context.Background(), manager, update.LauncherOptions{}, true, false, nil, stdout, stderr)
	if code != 1 || bytes.Contains(stdout.Bytes(), []byte("已是最新")) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}
