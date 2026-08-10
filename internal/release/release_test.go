package release

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestLauncherAssetNamingAndCUDACompanion(t *testing.T) {
	launcherRelease := GitHubRelease{Tag: "v2.3.4", Assets: []Asset{{Name: "llamalc-windows-amd64-v2.3.4.zip", Digest: digest}}}
	if _, err := LauncherAsset(launcherRelease, "windows", "amd64"); err != nil {
		t.Fatal(err)
	}
	llamaRelease := GitHubRelease{Tag: "b123", Assets: []Asset{{Name: "llama-b123-bin-win-cuda-12.4-x64.zip", Digest: digest}, {Name: "cudart-llama-bin-win-cuda-12.4-x64.zip", Digest: digest}}}
	backends, err := LlamaAssets(llamaRelease, "windows", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(backends) != 1 || len(backends[0].Assets) != 2 {
		t.Fatalf("backends=%+v", backends)
	}
}

func TestDownloadReportsProgressAndProtectsDisplayedURL(t *testing.T) {
	payload := bytes.Repeat([]byte("runtime"), 1024)
	hash := sha256.Sum256(payload)

	var events []DownloadEvent
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return downloadResponse(request, payload), nil
	})}, Progress: func(event DownloadEvent) { events = append(events, event) }}
	destination := filepath.Join(t.TempDir(), "runtime.zip")
	asset := Asset{Name: "runtime.zip", URL: "https://downloads.example/runtime.zip?token=secret", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	if err := client.Download(context.Background(), asset, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("downloaded=%d err=%v", len(got), err)
	}
	if len(events) < 3 || events[0].Phase != DownloadStart || events[len(events)-1].Phase != DownloadComplete {
		t.Fatalf("events=%+v", events)
	}
	if strings.Contains(events[0].URL, "secret") || events[len(events)-1].Downloaded != int64(len(payload)) || events[len(events)-1].SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("events=%+v", events)
	}
}

func TestDownloadReportsSystemProxyFallbackToDirect(t *testing.T) {
	payload := []byte("runtime archive")
	hash := sha256.Sum256(payload)
	proxyURL, _ := url.Parse("http://127.0.0.1:7890")
	proxyCalls := 0
	var phases []DownloadPhase
	client := &Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			proxyCalls++
			return nil, errors.New("proxy unavailable")
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return downloadResponse(request, payload), nil
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return proxyURL, nil },
		Progress:      func(event DownloadEvent) { phases = append(phases, event.Phase) },
	}
	asset := Asset{Name: "runtime.zip", URL: "https://downloads.example/runtime.zip", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	if err := client.Download(context.Background(), asset, filepath.Join(t.TempDir(), "runtime.zip")); err != nil {
		t.Fatal(err)
	}
	if proxyCalls != 1 || !containsPhase(phases, DownloadFallback) || phases[len(phases)-1] != DownloadComplete {
		t.Fatalf("proxyCalls=%d phases=%v", proxyCalls, phases)
	}
}

func TestDownloadPrefixProxyFallsBackToOriginalURL(t *testing.T) {
	payload := []byte("launcher archive")
	hash := sha256.Sum256(payload)
	var requested []string
	var phases []DownloadPhase
	client := &Client{
		Proxy: "https://proxy.example",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requested = append(requested, request.URL.String())
			return &http.Response{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway")), Request: request}, nil
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			requested = append(requested, request.URL.String())
			return downloadResponse(request, payload), nil
		})},
		Progress: func(event DownloadEvent) { phases = append(phases, event.Phase) },
	}
	asset := Asset{Name: "launcher.zip", URL: "https://downloads.example/launcher.zip", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	if err := client.Download(context.Background(), asset, filepath.Join(t.TempDir(), "launcher.zip")); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 2 || requested[0] != "https://proxy.example/https://downloads.example/launcher.zip" || requested[1] != asset.URL {
		t.Fatalf("requested=%v", requested)
	}
	if !containsPhase(phases, DownloadFallback) || phases[len(phases)-1] != DownloadComplete {
		t.Fatalf("phases=%v", phases)
	}
}

type interruptedReader struct {
	data []byte
	done bool
}

func (reader *interruptedReader) Read(buffer []byte) (int, error) {
	if !reader.done {
		reader.done = true
		return copy(buffer, reader.data), nil
	}
	return 0, errors.New("connection reset")
}

func TestDownloadInterruptedProxyRemovesPartialAndRetriesDirectOnce(t *testing.T) {
	payload := []byte("complete payload")
	hash := sha256.Sum256(payload)
	proxy, _ := url.Parse("http://127.0.0.1:7890")
	proxyCalls, directCalls := 0, 0
	client := &Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			proxyCalls++
			return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(&interruptedReader{data: payload[:4]}), Request: request}, nil
		})},
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			directCalls++
			return downloadResponse(request, payload), nil
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return proxy, nil },
	}
	destination := filepath.Join(t.TempDir(), "asset.zip")
	asset := Asset{Name: "asset.zip", URL: "https://example.invalid/asset.zip", Size: int64(len(payload)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	if err := client.Download(context.Background(), asset, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if proxyCalls != 1 || directCalls != 1 {
		t.Fatalf("proxy=%d direct=%d", proxyCalls, directCalls)
	}
}

func TestReleaseResponseRejectsTrailingJSONAndOversize(t *testing.T) {
	for name, body := range map[string]string{
		"trailing": `{"tag_name":"v1.2.3","assets":[]} {}`,
		"oversize": strings.Repeat(" ", int(maxReleaseResponse)+1),
	} {
		t.Run(name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}}
			if _, err := client.Latest(context.Background(), "owner/repo"); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestReleaseAPIPrefersDirectConnection(t *testing.T) {
	proxy, _ := url.Parse("http://127.0.0.1:7890")
	directCalls, proxyCalls := 0, 0
	client := &Client{
		DirectHTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			directCalls++
			body := `{"tag_name":"v1.2.3","assets":[]}`
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
		})},
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			proxyCalls++
			return nil, errors.New("proxy should not be used")
		})},
		ProxyResolver: func(*http.Request) (*url.URL, error) { return proxy, nil },
	}
	release, err := client.Latest(context.Background(), "owner/repo")
	if err != nil || release.Tag != "v1.2.3" {
		t.Fatalf("release=%+v err=%v", release, err)
	}
	if directCalls != 1 || proxyCalls != 0 {
		t.Fatalf("direct=%d proxy=%d", directCalls, proxyCalls)
	}
}

func TestExtractRejectsDuplicatePathsAndSharedEntryOverflow(t *testing.T) {
	makeZip := func(path string, names ...string) {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		writer := zip.NewWriter(file)
		for _, name := range names {
			entry, e := writer.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = entry.Write([]byte("x"))
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	duplicate := filepath.Join(t.TempDir(), "duplicate.zip")
	makeZip(duplicate, "same", "same")
	if err := Extract(duplicate, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("accepted duplicate archive path")
	}
	first, second := filepath.Join(t.TempDir(), "first.zip"), filepath.Join(t.TempDir(), "second.zip")
	makeZip(first, "one")
	makeZip(second, "two")
	budget := NewExtractBudget(1, 8<<30)
	if err := ExtractWithBudget(first, filepath.Join(t.TempDir(), "one"), budget); err != nil {
		t.Fatal(err)
	}
	if err := ExtractWithBudget(second, filepath.Join(t.TempDir(), "two"), budget); err == nil {
		t.Fatal("shared entry budget was reset")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func downloadResponse(request *http.Request, payload []byte) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
		Request:       request,
	}
}

func containsPhase(phases []DownloadPhase, wanted DownloadPhase) bool {
	for _, phase := range phases {
		if phase == wanted {
			return true
		}
	}
	return false
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

func TestExtractTarAllowsRelativeSymlinksAndRejectsEscapes(t *testing.T) {
	writeTar := func(path string, write func(*tar.Writer)) {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(file)
		tw := tar.NewWriter(gz)
		write(tw)
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	addReg := func(tw *tar.Writer, name, body string) {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	addSym := func(tw *tar.Writer, name, target string) {
		header := &tar.Header{Name: name, Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: target}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}

	good := filepath.Join(t.TempDir(), "good.tar.gz")
	writeTar(good, func(tw *tar.Writer) {
		addReg(tw, "lib/libfoo.so.1.0", "shared")
		addSym(tw, "lib/libfoo.so.1", "libfoo.so.1.0")
		addSym(tw, "lib/libfoo.so", "libfoo.so.1")
	})
	out := filepath.Join(t.TempDir(), "out")
	if err := Extract(good, out); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lib/libfoo.so", "lib/libfoo.so.1"} {
		info, err := os.Lstat(filepath.Join(out, filepath.FromSlash(name)))
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s not a symlink: info=%v err=%v", name, info, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(out, "lib", "libfoo.so"))
	if err != nil || string(body) != "shared" {
		t.Fatalf("symlink resolution body=%q err=%v", body, err)
	}

	escape := filepath.Join(t.TempDir(), "escape.tar.gz")
	writeTar(escape, func(tw *tar.Writer) {
		addSym(tw, "bad", "../outside")
	})
	if err := Extract(escape, filepath.Join(t.TempDir(), "escape-out")); err == nil {
		t.Fatal("accepted escaping symlink")
	}

	absolute := filepath.Join(t.TempDir(), "abs.tar.gz")
	writeTar(absolute, func(tw *tar.Writer) {
		addSym(tw, "bad", "/etc/passwd")
	})
	if err := Extract(absolute, filepath.Join(t.TempDir(), "abs-out")); err == nil {
		t.Fatal("accepted absolute symlink")
	}

	hard := filepath.Join(t.TempDir(), "hard.tar.gz")
	writeTar(hard, func(tw *tar.Writer) {
		addReg(tw, "a", "x")
		header := &tar.Header{Name: "b", Mode: 0o644, Typeflag: tar.TypeLink, Linkname: "a"}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	})
	if err := Extract(hard, filepath.Join(t.TempDir(), "hard-out")); err == nil || !strings.Contains(err.Error(), "硬链接") {
		t.Fatalf("hard link err=%v", err)
	}
}
