package launcher

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

func TestUpdateStateRejectsUnsafeCleanupTargets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	state := UpdateState{
		Schema: 1, LlamaTag: "b2", Backend: "cpu", ActiveRuntime: "data/llama.cpp/b2-cpu",
		Assets: []InstalledAsset{{Name: "a.zip", SHA256: testDigest}},
	}
	for _, pending := range [][]string{
		{"data/llama.cpp/b2-cpu"},
		{"data/llama.cpp/user-files"},
		{"data/llama.cpp/b1-cpu/nested"},
		{"data/llama.cpp/b1-cpu", "data/llama.cpp/b1-cpu"},
	} {
		state.PendingCleanup = pending
		if err := ValidateUpdateState(root, state); err == nil {
			t.Fatalf("unsafe pending cleanup accepted: %v", pending)
		}
	}
}

func TestWriteUpdateStateValidatesConfigPathBeforeCreating(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	external := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, ConfigDirectoryName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	state := UpdateState{
		Schema: 1, LlamaTag: "b1", Backend: "cpu", ActiveRuntime: "data/llama.cpp/b1-cpu",
		Assets: []InstalledAsset{{Name: "a.zip", SHA256: testDigest}},
	}
	if err := WriteUpdateState(root, state); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("symlink config directory accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(external, UpdateStateName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write escaped through config symlink: %v", err)
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

func TestTruncatedArchiveRemovesPartialExtractedFile(t *testing.T) {
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: "partial.bin", Mode: 0o600, Size: 64, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	// Bypass tar.Writer so the valid header advertises more bytes than exist.
	if _, err := raw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "truncated.tar.gz")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(archive)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := ExtractArchive(archivePath, destination, newExtractBudget(), io.Discard); err == nil {
		t.Fatal("truncated TAR unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(destination, "partial.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial extracted file remained: %v", err)
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

func TestDownloadProgressBar(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 2048)
	hash := sha256.Sum256(payload)
	client := &GitHubClient{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Request: request}, nil
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	asset := GitHubAsset{Name: "runtime.tar.gz", BrowserDownloadURL: "https://downloads.example/runtime.tar.gz", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	output := &bytes.Buffer{}
	if _, err := client.Download(context.Background(), asset, filepath.Join(t.TempDir(), "runtime.tar.gz"), output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"下载链接: https://downloads.example/runtime.tar.gz", "下载开始", "[==============================]", "100%", "2.0 KiB/2.0 KiB", "/s", "下载完成"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("progress output missing %q: %q", want, output.String())
		}
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

func TestManagedLlamaVersionDisplay(t *testing.T) {
	app := &Application{LlamaTag: "b10015", LlamaBackend: "cuda-13.3", LlamaVersion: "version: 10015 (abc123)"}
	want := "b10015 / cuda-13.3 — version: 10015 (abc123)"
	if got := app.llamaVersionDisplay(); got != want {
		t.Fatalf("llama version display = %q, want %q", got, want)
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

func TestParseSHA256SUMSAcceptsCurrentDirectoryPrefix(t *testing.T) {
	name := "llama-launcher-v0.0.2-windows-amd64.zip"
	data := []byte(testDigest + "  ./" + name + "\n")
	checksums, err := parseSHA256SUMS(data)
	if err != nil || checksums[name] != testDigest {
		t.Fatalf("current-directory checksum entry rejected: checksums=%#v err=%v", checksums, err)
	}
	for _, unsafeName := range []string{"../escape.zip", "subdir/file.zip", `/absolute.zip`, `dir\\file.zip`} {
		if _, err := parseSHA256SUMS([]byte(testDigest + "  " + unsafeName + "\n")); err == nil {
			t.Fatalf("unsafe checksum filename accepted: %q", unsafeName)
		}
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

func TestAPIFallsBackToDirectAfterProxyFailure(t *testing.T) {
	proxyURL, _ := url.Parse("http://127.0.0.1:7890")
	proxyCalls, directCalls := 0, 0
	client := &GitHubClient{
		APIBase: "https://api.github.com",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			proxyCalls++
			return nil, errors.New("proxy unavailable")
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			directCalls++
			body := `{"tag_name":"b123","assets":[]}`
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return proxyURL, nil },
	}
	release, err := client.Release(context.Background(), llamaRepository, "")
	if err != nil || release.TagName != "b123" || proxyCalls != 1 || directCalls != 1 {
		t.Fatalf("API fallback failed: release=%#v err=%v proxy=%d direct=%d", release, err, proxyCalls, directCalls)
	}
}

func TestDownloadFallsBackToDirectAfterProxyInterruption(t *testing.T) {
	payload := []byte("complete direct payload")
	hash := sha256.Sum256(payload)
	proxyURL, _ := url.Parse("http://127.0.0.1:7890")
	client := &GitHubClient{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(&failingReader{}), ContentLength: int64(len(payload)), Request: request}, nil
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Request: request}, nil
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return proxyURL, nil },
	}
	asset := GitHubAsset{Name: "runtime.tar.gz", BrowserDownloadURL: "https://downloads.example/runtime.tar.gz", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	destination := filepath.Join(t.TempDir(), "runtime.tar.gz")
	output := &bytes.Buffer{}
	if _, err := client.Download(context.Background(), asset, destination, output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, payload) || !strings.Contains(output.String(), "直连重试") {
		t.Fatalf("download fallback mismatch: data=%q err=%v output=%q", data, err, output)
	}
}

func TestNoProxyDoesNotPerformFallbackRetry(t *testing.T) {
	directCalls := 0
	client := &GitHubClient{
		APIBase: "https://api.github.com",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("direct connection failed")
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			directCalls++
			return nil, errors.New("unexpected retry")
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	if _, err := client.Release(context.Background(), llamaRepository, ""); err == nil || directCalls != 0 {
		t.Fatalf("no-proxy request retried: err=%v direct=%d", err, directCalls)
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

func makeLauncherTar(t *testing.T, launcher, updater []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	writer := tar.NewWriter(gz)
	for name, content := range map[string][]byte{
		"llama.cpp/bin/llama-launcher": launcher,
		"llama.cpp/bin/llama-updater":  updater,
	} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(content); err != nil {
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

func TestLauncherArchiveRequiresLauncherAndUpdaterOnly(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "llama.cpp", "bin", "llama-launcher")
	updater := filepath.Join(root, "llama.cpp", "bin", "llama-updater")
	touchFile(t, launcher)
	if err := ensureOnlyLauncherFiles(root, launcher, updater); err == nil {
		t.Fatal("archive without updater was accepted")
	}
	touchFile(t, updater)
	if err := ensureOnlyLauncherFiles(root, launcher, updater); err != nil {
		t.Fatalf("valid two-file archive rejected: %v", err)
	}
	extra := filepath.Join(root, "llama.cpp", "bin", "extra")
	touchFile(t, extra)
	if err := ensureOnlyLauncherFiles(root, launcher, updater); err == nil {
		t.Fatal("archive with an extra file was accepted")
	}
}

func TestCopyExecutableRemovesPartialDestinationOnFailure(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source-directory")
	if err := os.Mkdir(sourceDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, ".llama-updater-new-12345")
	if err := copyAndSyncExecutable(sourceDirectory, destination); err == nil {
		t.Fatal("copying a directory unexpectedly succeeded")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial executable remained after copy failure: %v", err)
	}
}

func makeWindowsRuntimeZIP(t *testing.T, tag string) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range []string{"llama-" + tag + "/llama-server.exe", "llama-" + tag + "/llama-cli.exe"} {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o755)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("fake executable")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestMaintenanceInstallContinuesIntoMainMenu(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	executable := filepath.Join(root, "bin", "llama-launcher.exe")
	touchFile(t, executable)
	staleUpdater := filepath.Join(root, "bin", ".llama-updater-run-12345.exe")
	touchFile(t, staleUpdater)
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return executable, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	payload := makeWindowsRuntimeZIP(t, "b1")
	payloadHash := sha256.Sum256(payload)
	asset := GitHubAsset{
		Name: "llama-b1-bin-win-cpu-x64.zip", Size: int64(len(payload)),
		Digest: "sha256:" + hex.EncodeToString(payloadHash[:]), BrowserDownloadURL: "https://downloads.example/runtime.zip",
	}
	releaseData, err := json.Marshal(GitHubRelease{TagName: "b1", Assets: []GitHubAsset{asset}})
	if err != nil {
		t.Fatal(err)
	}
	client := &GitHubClient{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := payload
			if request.URL.Hostname() == "api.github.com" {
				body = releaseData
			}
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request}, nil
		})},
		APIBase:       githubAPIBase,
		ProxyResolver: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	probe := &fakeInstallationProbe{}
	oldFactory := updateManagerFactory
	updateManagerFactory = func(factoryRoot string, factoryProbe InstallationProbe, stdout, stderr io.Writer) *UpdateManager {
		return &UpdateManager{
			Root: factoryRoot, GOOS: "windows", GOARCH: "amd64", Client: client,
			Probe: factoryProbe, LauncherProbe: factoryProbe, Stdout: stdout, Stderr: stderr,
		}
	}
	t.Cleanup(func() {
		updateManagerFactory = oldFactory
	})

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := mainWithProbe(nil, bytes.NewBufferString("1\n1\ny\nq\n"), stdout, stderr, &fakeExecutor{}, probe, "windows")
	if code != 0 {
		t.Fatalf("main returned %d: %s", code, stderr)
	}
	if _, err := os.Stat(staleUpdater); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale ephemeral updater was not cleaned on startup: %v", err)
	}
	for _, want := range []string{"llama.cpp 安装完成，正在进入主菜单", "实际探测文件:", "llama.cpp Go 启动器"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("post-install output missing %q: %s", want, stdout)
		}
	}
	if len(probe.commands) != 2 {
		t.Fatalf("probe count=%d, want install probe plus startup probe", len(probe.commands))
	}
}

func releaseWithRuntime(t *testing.T, tag string, payload []byte) GitHubRelease {
	t.Helper()
	hash := sha256.Sum256(payload)
	name := fmt.Sprintf("llama-%s-bin-ubuntu-x64.tar.gz", tag)
	return GitHubRelease{TagName: tag, Assets: []GitHubAsset{{
		Name: name, Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:]), BrowserDownloadURL: "https://downloads.example/" + name,
	}}}
}

func runtimeDownloadClient(payload []byte) *GitHubClient {
	return &GitHubClient{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(payload)), ContentLength: int64(len(payload)), Request: request,
		}, nil
	})}}
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

func TestForceUpdateRefusesUntrackedTargetDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{"b1": makeRuntimeTar(t, "b1"), "b2": makeRuntimeTar(t, "b2")}
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: runtimeDownloadClient(payloads["b1"]),
		Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard,
	}
	if err := manager.InstallLlama(context.Background(), releaseWithRuntime(t, "b1", payloads["b1"]), "cpu", false, false, false); err != nil {
		t.Fatal(err)
	}
	untracked := filepath.Join(managedRuntimeRoot(root), "b2-cpu")
	if err := os.Mkdir(untracked, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(untracked, "user-file")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.Client = runtimeDownloadClient(payloads["b2"])
	err := manager.InstallLlama(context.Background(), releaseWithRuntime(t, "b2", payloads["b2"]), "cpu", true, false, true)
	if err == nil || !strings.Contains(err.Error(), "拒绝覆盖") {
		t.Fatalf("untracked target was accepted: %v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("untracked target was changed: %q err=%v", content, err)
	}
}

func TestInstallValidatesRuntimePathBeforeCreating(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	external := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "data")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	payload := makeRuntimeTar(t, "b1")
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: runtimeDownloadClient(payload),
		Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard,
	}
	err := manager.InstallLlama(context.Background(), releaseWithRuntime(t, "b1", payload), "cpu", false, false, false)
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("runtime parent symlink accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(external, "llama.cpp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime directory escaped through symlink: %v", err)
	}
}

func TestRecoveryInstallPreservesOrphanedSameVersionRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := makeRuntimeTar(t, "b1")
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: runtimeDownloadClient(payload),
		Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard,
	}
	release := releaseWithRuntime(t, "b1", payload)
	if err := manager.InstallLlama(context.Background(), release, "cpu", false, false, false); err != nil {
		t.Fatal(err)
	}
	state, _, err := LoadUpdateState(root)
	if err != nil {
		t.Fatal(err)
	}
	oldRuntime := filepath.Join(root, filepath.FromSlash(state.ActiveRuntime))
	marker := filepath.Join(oldRuntime, "old-runtime-marker")
	if err := os.WriteFile(marker, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(UpdateStatePath(root)); err != nil {
		t.Fatal(err)
	}

	if err := installWithQuarantinedRuntime(context.Background(), manager, release, "cpu"); err != nil {
		t.Fatal(err)
	}
	state, exists, err := LoadUpdateState(root)
	if err != nil || !exists || state.LlamaTag != "b1" || state.Backend != "cpu" {
		t.Fatalf("bad recovered state: %#v exists=%v err=%v", state, exists, err)
	}
	recoveryMarker := filepath.Join(root, "data", "llama.cpp-recovery", "b1-cpu", "old-runtime-marker")
	if content, err := os.ReadFile(recoveryMarker); err != nil || string(content) != "old" {
		t.Fatalf("orphaned runtime was not preserved: %q err=%v", content, err)
	}
	entries, err := os.ReadDir(managedRuntimeRoot(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".orphan-") {
			t.Fatalf("successful recovery retained quarantine %s", entry.Name())
		}
	}
}

func TestInstallCommandDetectsOrphanAndConfirmsBeforeChangingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	marker := filepath.Join(managedRuntimeRoot(root), "legacy", "marker")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := makeRuntimeTar(t, "b1")
	release := releaseWithRuntime(t, "b1", payload)
	releaseData, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	downloadStarted := false
	client := &GitHubClient{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body := releaseData
			if request.URL.Hostname() != "api.github.com" {
				downloadStarted = true
				body = payload
			}
			return &http.Response{
				StatusCode: 200, Status: "200 OK", Header: make(http.Header),
				Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request,
			}, nil
		})},
		APIBase: githubAPIBase, ProxyResolver: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: client,
		Probe: probe, LauncherProbe: probe, Stdout: stdout, Stderr: stderr,
	}

	code, commandErr := runManagementCommand(
		context.Background(), manager, "install", []string{"--backend", "cpu"}, bytes.NewBufferString("n\n"), true,
	)
	if code != 1 || commandErr == nil || !strings.Contains(commandErr.Error(), "已取消") {
		t.Fatalf("code=%d err=%v", code, commandErr)
	}
	if !strings.Contains(stderr.String(), "未找到 config/update-state.json") || !strings.Contains(stdout.String(), "保留为恢复备份") {
		t.Fatalf("missing recovery warning or confirmation: stdout=%s stderr=%s", stdout, stderr)
	}
	if downloadStarted {
		t.Fatal("runtime download started before confirmation")
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("declined recovery changed runtime: content=%q err=%v", content, err)
	}
}

func TestRecoveryInstallFailureRestoresRuntimeAndState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	legacy := filepath.Join(managedRuntimeRoot(root), "b1-cpu")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalState := []byte("{broken-json")
	if err := os.MkdirAll(filepath.Dir(UpdateStatePath(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpdateStatePath(root), originalState, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := makeRuntimeTar(t, "b2")
	probe := &fakeInstallationProbe{err: errors.New("probe failed")}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: runtimeDownloadClient(payload),
		Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard,
	}

	err := installWithQuarantinedRuntime(context.Background(), manager, releaseWithRuntime(t, "b2", payload), "cpu")
	if err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("recovery install error=%v", err)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "keep" {
		t.Fatalf("original runtime was not restored: content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(UpdateStatePath(root)); err != nil || !bytes.Equal(content, originalState) {
		t.Fatalf("original state was not restored: content=%q err=%v", content, err)
	}
}

func TestRecoveryInstallPreservesUnknownRuntimeAndCorruptState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	legacy := filepath.Join(managedRuntimeRoot(root), "legacy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(legacy, "user-file")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(UpdateStatePath(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpdateStatePath(root), []byte("{broken-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := makeRuntimeTar(t, "b2")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	probe := &fakeInstallationProbe{}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: runtimeDownloadClient(payload),
		Probe: probe, LauncherProbe: probe, Stdout: stdout, Stderr: stderr,
	}

	if err := installWithQuarantinedRuntime(context.Background(), manager, releaseWithRuntime(t, "b2", payload), "cpu"); err != nil {
		t.Fatal(err)
	}
	state, exists, err := LoadUpdateState(root)
	if err != nil || !exists || len(state.PendingCleanup) != 0 {
		t.Fatalf("recovered state=%#v exists=%v err=%v", state, exists, err)
	}
	recovery := filepath.Join(root, "data", "llama.cpp-recovery")
	if content, err := os.ReadFile(filepath.Join(recovery, "legacy", "user-file")); err != nil || string(content) != "keep" {
		t.Fatalf("unknown runtime file was not preserved: %q err=%v", content, err)
	}
	matches, err := filepath.Glob(filepath.Join(recovery, ".update-state.json.corrupt*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt state was not preserved in recovery directory: %v err=%v", matches, err)
	}
	if !strings.Contains(stdout.String(), "旧目录未自动删除") {
		t.Fatalf("recovery preservation was not reported: stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestRecoveryRefusesSymlinkWithoutChangingRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	runtimeRoot := managedRuntimeRoot(root)
	if err := os.MkdirAll(runtimeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(runtimeRoot, "unsafe-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := createInstallRecoveryQuarantine(root)
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("unsafe runtime accepted: %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != outside {
		t.Fatalf("unsafe runtime was changed: target=%q err=%v", target, err)
	}
}

func TestCleanupLauncherTempsOnlyRemovesRegularEphemeralUpdater(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ephemeral := filepath.Join(bin, ".llama-updater-run-12345.exe")
	legacy := filepath.Join(bin, "llama-updater.exe")
	unrelated := filepath.Join(bin, "keep.exe")
	unsafeDirectory := filepath.Join(bin, ".llama-updater-run-67890.exe")
	unmarkedDirectory := filepath.Join(bin, ".launcher-update-12345")
	markedDirectory := filepath.Join(bin, ".launcher-update-67890")
	nonGeneratedName := filepath.Join(bin, ".llama-launcher-new-user-notes.exe")
	touchFile(t, ephemeral)
	touchFile(t, legacy)
	touchFile(t, unrelated)
	touchFile(t, nonGeneratedName)
	if err := os.Mkdir(unsafeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(unmarkedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(unmarkedDirectory, "user-file"))
	if err := os.Mkdir(markedDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := markManagedTempDirectory(bin, markedDirectory); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(markedDirectory, "download.part"))
	stderr := &bytes.Buffer{}
	cleanupLauncherTemps(root, stderr)
	if _, err := os.Stat(ephemeral); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral updater residual was not removed: %v", err)
	}
	for _, path := range []string{legacy, unrelated, unsafeDirectory, unmarkedDirectory, nonGeneratedName} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup removed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(markedDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked staging directory was not removed: %v", err)
	}
	if !strings.Contains(stderr.String(), "拒绝清理不是普通文件") || !strings.Contains(stderr.String(), "所有权标记") {
		t.Fatalf("unsafe residual warning missing: %s", stderr)
	}
}

func TestCleanupRuntimeTempsRequiresOwnershipMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	base := managedRuntimeRoot(root)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	marked := filepath.Join(base, ".staging-12345")
	unmarked := filepath.Join(base, ".staging-67890")
	for _, path := range []string{marked, unmarked} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := markManagedTempDirectory(base, marked); err != nil {
		t.Fatal(err)
	}
	touchFile(t, filepath.Join(marked, "partial-download"))
	touchFile(t, filepath.Join(unmarked, "user-file"))
	stderr := &bytes.Buffer{}
	cleanupRuntimeTemps(root, stderr)
	if _, err := os.Stat(marked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked runtime staging was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unmarked, "user-file")); err != nil {
		t.Fatalf("unmarked directory was changed: %v", err)
	}
	if !strings.Contains(stderr.String(), "所有权标记") {
		t.Fatalf("unmarked directory warning missing: %s", stderr)
	}
}

func TestPendingCleanupRefusesRegularFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	path := filepath.Join(managedRuntimeRoot(root), "b1-cpu")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := UpdateState{PendingCleanup: []string{"data/llama.cpp/b1-cpu"}}
	stderr := &bytes.Buffer{}
	manager := &UpdateManager{Root: root, Stderr: stderr}
	manager.retryPendingCleanup(&state)
	if len(state.PendingCleanup) != 1 {
		t.Fatal("unsafe cleanup entry was removed from retry state")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "user data" {
		t.Fatalf("regular user file was changed: %q err=%v", content, err)
	}
	if !strings.Contains(stderr.String(), "不是普通目录") {
		t.Fatalf("unsafe cleanup warning missing: %s", stderr)
	}
}

func TestStandaloneUpdaterReplacesFixedLauncherTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "llama.cpp")
	launcherPath := filepath.Join(root, "bin", "llama-launcher")
	updaterPath := filepath.Join(root, "bin", "llama-updater")
	if err := os.MkdirAll(filepath.Dir(launcherPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcherPath, []byte("old launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updaterPath, []byte("old updater"), 0o755); err != nil {
		t.Fatal(err)
	}
	tag := "v1.2.3"
	archiveName := launcherAssetName(tag, "linux", "amd64")
	newLauncher := []byte("new launcher")
	newUpdater := []byte("new updater")
	archiveData := makeLauncherTar(t, newLauncher, newUpdater)
	archiveHash := sha256.Sum256(archiveData)
	sumsData := []byte(hex.EncodeToString(archiveHash[:]) + "  " + archiveName + "\n")
	sumsHash := sha256.Sum256(sumsData)
	release := GitHubRelease{TagName: tag, Assets: []GitHubAsset{
		{Name: archiveName, Size: int64(len(archiveData)), Digest: "sha256:" + hex.EncodeToString(archiveHash[:]), BrowserDownloadURL: "https://downloads.example/launcher"},
		{Name: "SHA256SUMS.txt", Size: int64(len(sumsData)), Digest: "sha256:" + hex.EncodeToString(sumsHash[:]), BrowserDownloadURL: "https://downloads.example/sums"},
	}}
	client := &GitHubClient{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := archiveData
		if strings.Contains(request.URL.Path, "sums") {
			body = sumsData
		}
		return &http.Response{
			StatusCode: 200, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: request,
		}, nil
	})}}
	probe := &fakeInstallationProbe{output: "Version:   " + tag + "\nCommit: test\n"}
	manager := &UpdateManager{
		Root: root, GOOS: "linux", GOARCH: "amd64", Client: client,
		Probe: probe, LauncherProbe: probe, Stdout: io.Discard, Stderr: io.Discard,
	}

	if err := manager.UpdateLauncher(context.Background(), release, false, false); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(launcherPath); err != nil || !bytes.Equal(content, newLauncher) {
		t.Fatalf("launcher target content=%q err=%v", content, err)
	}
	if content, err := os.ReadFile(updaterPath); err != nil || !bytes.Equal(content, newUpdater) {
		t.Fatalf("updater target content=%q err=%v", content, err)
	}
}
