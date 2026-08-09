package release

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestLauncherAssetNamingAndCUDACompanion(t *testing.T) {
	r := GitHubRelease{Tag: "v2.3.4", Assets: []Asset{{Name: "llamalc-windows-amd64-v2.3.4.zip", Digest: digest}, {Name: "llama-b123-bin-win-cuda-12.4-x64.zip", Digest: digest}, {Name: "cudart-llama-bin-win-cuda-12.4-x64.zip", Digest: digest}}}
	if _, err := LauncherAsset(r, "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	backends, err := LlamaAssets(r, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 1 || len(backends[0].Assets) != 2 {
		t.Fatalf("backends=%+v", backends)
	}
}
func TestExtractRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	entry, err := w.Create("../escape")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("x"))
	_ = w.Close()
	_ = f.Close()
	destination := t.TempDir()
	if err = Extract(archive, destination); err == nil || !strings.Contains(err.Error(), "受管根目录") {
		t.Fatalf("err=%v", err)
	}
}
