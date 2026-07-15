package launcher

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testAsset(name string) GitHubAsset {
	return GitHubAsset{Name: name, Size: 100, Digest: "sha256:" + testDigest, BrowserDownloadURL: "https://example.invalid/" + name}
}

func TestResolveLlamaAssetsRealNamingSamples(t *testing.T) {
	release := GitHubRelease{TagName: "b9637", Assets: []GitHubAsset{
		testAsset("llama-b9637-bin-win-cpu-x64.zip"),
		testAsset("llama-b9637-bin-win-vulkan-x64.zip"),
		testAsset("llama-b9637-bin-win-cuda-12.4-x64.zip"),
		testAsset("cudart-llama-bin-win-cuda-12.4-x64.zip"),
		testAsset("llama-b9637-bin-win-cuda-13.3-x64.zip"), // no runtime companion
		testAsset("llama-b9637-bin-win-cpu-arm64.zip"),
	}}
	options, err := ResolveLlamaAssets(release, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(backendIDs(options), ","); got != "cpu,cuda-12.4,vulkan" {
		t.Fatalf("backends = %s", got)
	}
	cuda, err := SelectBackend(options, "cuda-12.4")
	if err != nil || len(cuda.Assets) != 2 {
		t.Fatalf("CUDA bundle = %#v, err=%v", cuda, err)
	}

	linux := GitHubRelease{TagName: "b9637", Assets: []GitHubAsset{
		testAsset("llama-b9637-bin-ubuntu-x64.tar.gz"),
		testAsset("llama-b9637-bin-ubuntu-vulkan-x64.tar.gz"),
		testAsset("llama-b9637-bin-ubuntu-vulkan-arm64.tar.gz"),
	}}
	options, err = ResolveLlamaAssets(linux, "linux", "amd64")
	if err != nil || strings.Join(backendIDs(options), ",") != "cpu,vulkan" {
		t.Fatalf("Linux options=%#v err=%v", options, err)
	}
}

func TestResolveLlamaAssetsRequiresDigestAndExactBackend(t *testing.T) {
	release := GitHubRelease{TagName: "b1", Assets: []GitHubAsset{{Name: "llama-b1-bin-ubuntu-x64.tar.gz"}}}
	if _, err := ResolveLlamaAssets(release, "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("missing digest accepted: %v", err)
	}
	release.Assets[0].Digest = "sha256:" + testDigest
	options, _ := ResolveLlamaAssets(release, "linux", "amd64")
	if _, err := SelectBackend(options, "vulkan"); err == nil || !strings.Contains(err.Error(), "可用值") {
		t.Fatalf("missing backend accepted: %v", err)
	}
}

func TestVersionComparisons(t *testing.T) {
	if comparison, err := CompareLlamaTags("b9", "b10"); err != nil || comparison >= 0 {
		t.Fatalf("llama comparison=%d err=%v", comparison, err)
	}
	if comparison, err := CompareSemVer("v1.9.0", "v1.10.0"); err != nil || comparison >= 0 {
		t.Fatalf("semver comparison=%d err=%v", comparison, err)
	}
	if _, err := CompareLlamaTags("v1", "b2"); err == nil {
		t.Fatal("invalid llama tag accepted")
	}
}

func TestUpdateStateRejectsEscapesAndSymlinkRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	state := UpdateState{Schema: 1, LlamaTag: "b1", Backend: "cpu", ActiveRuntime: "/tmp/runtime", Assets: []InstalledAsset{{Name: "a.zip", SHA256: testDigest}}}
	if err := ValidateUpdateState(root, state); err == nil {
		t.Fatal("absolute runtime accepted")
	}
	state.ActiveRuntime = "data/llama.cpp/b1-cpu"
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "data", "llama.cpp")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := ValidateUpdateState(root, state); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("symlink runtime accepted: %v", err)
	}
}

func TestSafeArchiveRejectsZIPSlipDuplicateAndTarLink(t *testing.T) {
	makeZIP := func(entries []string) string {
		path := filepath.Join(t.TempDir(), "bad.zip")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		for _, name := range entries {
			entry, err := writer.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = entry.Write([]byte("x"))
		}
		_ = writer.Close()
		_ = file.Close()
		return path
	}
	for _, entries := range [][]string{{"../escape"}, {"same", "same"}} {
		if err := ExtractArchive(makeZIP(entries), t.TempDir(), newExtractBudget(), io.Discard); err == nil {
			t.Fatalf("malicious ZIP accepted: %v", entries)
		}
	}

	tarPath := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, _ := os.Create(tarPath)
	gz := gzip.NewWriter(file)
	writer := tar.NewWriter(gz)
	_ = writer.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../outside"})
	_ = writer.Close()
	_ = gz.Close()
	_ = file.Close()
	if err := ExtractArchive(tarPath, t.TempDir(), newExtractBudget(), io.Discard); err == nil || !strings.Contains(err.Error(), "链接") {
		t.Fatalf("TAR link accepted: %v", err)
	}
}

func TestDownloadUsesTLSAndVerifiesDigest(t *testing.T) {
	payload := []byte("verified download")
	hash := sha256.Sum256(payload)
	client := &GitHubClient{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Request: request,
		}, nil
	})}}
	asset := GitHubAsset{Name: "a.zip", BrowserDownloadURL: "https://downloads.example/a.zip", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	destination := filepath.Join(t.TempDir(), "asset")
	if _, err := client.Download(context.Background(), asset, destination, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(destination)
	if !bytes.Equal(data, payload) {
		t.Fatal("download content mismatch")
	}
	asset.Digest = "sha256:" + testDigest
	if _, err := client.Download(context.Background(), asset, filepath.Join(t.TempDir(), "bad"), io.Discard); err == nil || !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("bad digest accepted: %v", err)
	}
	asset.BrowserDownloadURL = "http://example.invalid/a.zip"
	if _, err := client.Download(context.Background(), asset, filepath.Join(t.TempDir(), "http"), io.Discard); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("HTTP accepted: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestMaintenanceModeIgnoresFlatLegacyRuntime(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "llama.cpp")
	executable := filepath.Join(root, "bin", "llama-launcher.exe")
	touchFile(t, executable)
	touchFile(t, filepath.Join(root, "llama-server.exe"))
	old := executablePath
	executablePath = func() (string, error) { return executable, nil }
	t.Cleanup(func() { executablePath = old })
	output, errorsOutput := &bytes.Buffer{}, &bytes.Buffer{}
	probe := &fakeInstallationProbe{}
	code := mainWithProbe(nil, bytes.NewBufferString("q\n"), output, errorsOutput, &fakeExecutor{}, probe, "windows")
	if code != 0 || !strings.Contains(output.String(), "维护模式") || len(probe.commands) != 0 {
		t.Fatalf("flat runtime was used: code=%d output=%q stderr=%q probes=%d", code, output, errorsOutput, len(probe.commands))
	}
}

func TestRootMustBeLiterallyNamedLlamaCpp(t *testing.T) {
	_, err := launcherRootFromExecutable(filepath.Join(t.TempDir(), "bin", "llama-launcher"))
	if err == nil || !strings.Contains(err.Error(), "字面命名") {
		t.Fatalf("wrong root accepted: %v", err)
	}
}

func TestGitHubReleaseLatestTagTokenAndRateLimit(t *testing.T) {
	var paths []string
	client := &GitHubClient{
		APIBase: "https://api.github.com",
		Token:   "secret-token",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			paths = append(paths, request.URL.Path)
			if request.Header.Get("Authorization") != "Bearer secret-token" {
				t.Fatalf("API token missing: %q", request.Header.Get("Authorization"))
			}
			body := `{"tag_name":"b123","assets":[]}`
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})},
	}
	if _, err := client.Release(context.Background(), llamaRepository, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Release(context.Background(), llamaRepository, "b123"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/latest") || !strings.HasSuffix(paths[1], "/tags/b123") {
		t.Fatalf("unexpected API paths: %#v", paths)
	}

	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("X-RateLimit-Remaining", "0")
		header.Set("X-RateLimit-Reset", "12345")
		return &http.Response{StatusCode: 403, Status: "403 Forbidden", Header: header, Body: io.NopCloser(strings.NewReader(`{"message":"rate limit"}`)), Request: request}, nil
	})}
	if _, err := client.Release(context.Background(), llamaRepository, ""); err == nil || !strings.Contains(err.Error(), "限流") || !strings.Contains(err.Error(), "12345") {
		t.Fatalf("rate limit not reported: %v", err)
	}

	client.APIBase = "https://github.example/api"
	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "" {
			t.Fatal("token leaked to non-api.github.com host")
		}
		body := `{"tag_name":"b123","assets":[]}`
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	if _, err := client.Release(context.Background(), llamaRepository, ""); err != nil {
		t.Fatal(err)
	}
}

type failingReader struct{ sent bool }

func (reader *failingReader) Read(data []byte) (int, error) {
	if !reader.sent {
		reader.sent = true
		copy(data, "partial")
		return len("partial"), nil
	}
	return 0, errors.New("connection reset")
}

func TestDownloadInterruptionRemovesPartialFile(t *testing.T) {
	client := &GitHubClient{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(&failingReader{}), ContentLength: 100, Request: request}, nil
	})}}
	asset := testAsset("broken.tar.gz")
	destination := filepath.Join(t.TempDir(), "broken.tar.gz")
	if _, err := client.Download(context.Background(), asset, destination, io.Discard); err == nil || !strings.Contains(err.Error(), "中断") {
		t.Fatalf("interruption accepted: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial download remains: %v", err)
	}
}

func makeRuntimeTar(t *testing.T, tag string) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	writer := tar.NewWriter(gz)
	for _, name := range []string{"llama-" + tag + "/llama-server", "llama-" + tag + "/llama-cli"} {
		data := []byte("fake executable")
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func releaseWithRuntime(t *testing.T, tag string, payload []byte) GitHubRelease {
	t.Helper()
	hash := sha256.Sum256(payload)
	name := fmt.Sprintf("llama-%s-bin-ubuntu-x64.tar.gz", tag)
	return GitHubRelease{TagName: tag, Assets: []GitHubAsset{{
		Name: name, Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:]), BrowserDownloadURL: "https://downloads.example/" + name,
	}}}
}

func TestManagedRuntimeInstallAndAtomicVersionSwitch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{"b1": makeRuntimeTar(t, "b1"), "b2": makeRuntimeTar(t, "b2")}
	currentPayload := payloads["b1"]
	client := &GitHubClient{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(currentPayload)), ContentLength: int64(len(currentPayload)), Request: request}, nil
	})}}
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{Root: root, GOOS: "linux", GOARCH: "amd64", Client: client, Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard}
	if err := manager.InstallLlama(context.Background(), releaseWithRuntime(t, "b1", currentPayload), "cpu", false, false, false); err != nil {
		t.Fatal(err)
	}
	state, exists, err := LoadUpdateState(root)
	if err != nil || !exists || state.LlamaTag != "b1" || state.Backend != "cpu" {
		t.Fatalf("bad installed state: %#v exists=%v err=%v", state, exists, err)
	}
	oldRuntime := filepath.Join(root, filepath.FromSlash(state.ActiveRuntime))
	paths, err := ResolveManagedPaths(root, "linux", state)
	if err != nil || !strings.Contains(paths.Server, filepath.Join("data", "llama.cpp", "b1-cpu")) {
		t.Fatalf("bad managed paths: %#v err=%v", paths, err)
	}

	currentPayload = payloads["b2"]
	if err := manager.InstallLlama(context.Background(), releaseWithRuntime(t, "b2", currentPayload), "", false, false, true); err != nil {
		t.Fatal(err)
	}
	state, _, err = LoadUpdateState(root)
	if err != nil || state.LlamaTag != "b2" || state.Backend != "cpu" {
		t.Fatalf("bad updated state: %#v err=%v", state, err)
	}
	if _, err := os.Stat(oldRuntime); !os.IsNotExist(err) {
		t.Fatalf("old runtime was retained: %v", err)
	}
	if len(probe.commands) != 2 {
		t.Fatalf("runtime probe count=%d", len(probe.commands))
	}
}
