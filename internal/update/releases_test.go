package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Snail-one/LlamaLc/internal/layout"
	"github.com/Snail-one/LlamaLc/internal/release"
)

type llamaReleaseFixture struct {
	releases  map[string]release.GitHubRelease
	downloads map[string][]byte
	requests  []string
}

func (f *llamaReleaseFixture) RoundTrip(req *http.Request) (*http.Response, error) {
	path := req.URL.Path
	f.requests = append(f.requests, path)
	status := http.StatusOK
	var data []byte
	base := "/repos/" + LlamaRepository + "/releases"
	if path == base {
		data, _ = json.Marshal([]release.GitHubRelease{f.releases[base+"/latest"], f.releases[base+"/tags/b10333"]})
	} else if rel, ok := f.releases[path]; ok {
		data, _ = json.Marshal(rel)
	} else if body, ok := f.downloads[path]; ok {
		data = body
	} else {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), Request: req}, nil
}

func fixtureAsset(name string, data []byte) release.Asset {
	digest := sha256.Sum256(data)
	return release.Asset{Name: name, URL: "https://example.invalid/" + name, Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(digest[:])}
}

func newLlamaReleaseFixture(t *testing.T) (*Manager, *llamaReleaseFixture) {
	t.Helper()
	base := "/repos/" + LlamaRepository + "/releases/"
	f := &llamaReleaseFixture{
		releases: map[string]release.GitHubRelease{
			base + "latest":      {Tag: "v0.7.0", Assets: []release.Asset{fixtureAsset("nightly-tag.txt", []byte("b10332\n"))}},
			base + "tags/v0.7.0": {Tag: "v0.7.0", Assets: []release.Asset{fixtureAsset("nightly-tag.txt", []byte("b10332\n"))}},
			base + "tags/b10332": {Tag: "b10332", Prerelease: true, Assets: []release.Asset{
				fixtureAsset("llama-b10332-bin-ubuntu-x64.tar.gz", []byte("x")),
				fixtureAsset("llama-b10332-bin-win-x64.zip", []byte("x")),
			}},
			base + "tags/b10333": {Tag: "b10333", Prerelease: true, Assets: []release.Asset{
				fixtureAsset("llama-b10333-bin-ubuntu-x64.tar.gz", []byte("x")),
				fixtureAsset("llama-b10333-bin-win-x64.zip", []byte("x")),
			}},
			"/repos/" + LauncherRepository + "/releases/latest": {Tag: "v1.0.0"},
		},
		downloads: map[string][]byte{"/nightly-tag.txt": []byte("b10332\n")},
	}
	l, err := layout.New(filepath.Join(t.TempDir(), "LlamaLc"), "linux")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(l, &release.Client{HTTP: &http.Client{Transport: f}})
	m.GOOS, m.GOARCH = "linux", "amd64"
	return m, f
}

func TestLlamaStableChannelAndExplicitSemanticVersion(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, version := range []string{"", "v0.7.0", "b10332"} {
			t.Run(platform+"/"+version, func(t *testing.T) {
				m, _ := newLlamaReleaseFixture(t)
				m.GOOS = platform
				client := m.Source.(*release.Client)
				client.Progress = func(release.DownloadEvent) { t.Error("metadata download emitted progress") }
				plan, err := m.PrepareLlama(context.Background(), LlamaOptions{Channel: release.ChannelStable, Version: version, Backend: "cpu"})
				if err != nil {
					t.Fatal(err)
				}
				if plan.Release.Tag != "b10332" || plan.Options.Channel != release.ChannelStable || filepath.Base(plan.Target) != "b10332" {
					t.Fatalf("plan=%+v", plan)
				}
				if version != "b10332" && plan.Release.ReleaseTag != "v0.7.0" {
					t.Fatalf("semantic version lost: %+v", plan.Release)
				}
			})
		}
	}
	m, _ := newLlamaReleaseFixture(t)
	results, err := m.CheckWithChannel(context.Background(), "all", release.ChannelStable)
	if err != nil || len(results) != 2 || results[0].Latest != "b10332" || results[0].Release != "v0.7.0" || results[0].Channel != release.ChannelStable || results[1].Latest != "v1.0.0" {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	tag, ids, _, err := m.AvailableLlamaBackendsWithChannel(context.Background(), release.ChannelStable)
	if err != nil || tag != "b10332" || len(ids) != 1 || ids[0] != "cpu" {
		t.Fatalf("tag=%s ids=%v err=%v", tag, ids, err)
	}
}

func TestLlamaStableChannelRetainsDowngradeProtection(t *testing.T) {
	m, _ := newLlamaReleaseFixture(t)
	state := State{Schema: StateSchema, LlamaTag: "b10333", Backend: "cpu", ActiveRuntime: runtimeRelative("cpu", "b10333"), Assets: []InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}}
	if err := SaveState(m.Layout, state); err != nil {
		t.Fatal(err)
	}
	results, err := m.CheckWithChannel(context.Background(), "llama", release.ChannelStable)
	if err != nil || len(results) != 1 || results[0].Available {
		t.Fatalf("results=%+v err=%v", results, err)
	}
	if _, err := m.PrepareLlama(context.Background(), LlamaOptions{Channel: release.ChannelStable}); err == nil || !strings.Contains(err.Error(), "降级") {
		t.Fatalf("downgrade not rejected: %v", err)
	}
	if _, err := m.PrepareLlama(context.Background(), LlamaOptions{Channel: release.ChannelStable, AllowDowngrade: true}); err != nil {
		t.Fatal(err)
	}
}

func TestLlamaStableRejectsInvalidReleasePointers(t *testing.T) {
	for _, name := range []string{"missing", "oversize", "digest", "checksum", "malformed", "loop", "missing-build", "draft-build", "prerelease-release"} {
		t.Run(name, func(t *testing.T) {
			m, f := newLlamaReleaseFixture(t)
			base := "/repos/" + LlamaRepository + "/releases/"
			rel := f.releases[base+"latest"]
			switch name {
			case "missing":
				rel.Assets = nil
			case "oversize":
				rel.Assets[0].Size = 129
			case "digest":
				rel.Assets[0].Digest = ""
			case "checksum":
				f.downloads["/nightly-tag.txt"] = []byte("b10333\n")
			case "malformed", "loop":
				data := []byte("../b10332\n")
				if name == "loop" {
					data = []byte("v0.7.0\n")
				}
				rel.Assets[0] = fixtureAsset("nightly-tag.txt", data)
				f.downloads["/nightly-tag.txt"] = data
			case "missing-build":
				delete(f.releases, base+"tags/b10332")
			case "draft-build":
				build := f.releases[base+"tags/b10332"]
				build.Draft = true
				f.releases[base+"tags/b10332"] = build
			case "prerelease-release":
				rel.Prerelease = true
			}
			f.releases[base+"latest"] = rel
			if _, err := m.CheckWithChannel(context.Background(), "llama", release.ChannelStable); err == nil {
				t.Fatal("accepted invalid stable release")
			}
		})
	}
}

func TestLlamaBuildPrereleaseInstallPlan(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, version := range []string{"", "latest", "b10333"} {
			t.Run(platform+"/"+version, func(t *testing.T) {
				m, f := newLlamaReleaseFixture(t)
				m.GOOS = platform
				plan, err := m.PrepareLlama(context.Background(), LlamaOptions{Version: version, Backend: "cpu"})
				if err != nil {
					t.Fatal(err)
				}
				if plan.Release.Tag != "b10333" || plan.Backend.ID != "cpu" || filepath.Base(plan.Target) != "b10333" {
					t.Fatalf("unexpected plan: %+v", plan)
				}
				if len(f.requests) != 1 || strings.HasSuffix(f.requests[0], "/latest") {
					t.Fatalf("requests=%v", f.requests)
				}
			})
		}
	}
}

func TestLlamaBuildPrereleaseBackendCatalogAndChecks(t *testing.T) {
	m, _ := newLlamaReleaseFixture(t)
	tag, ids, current, err := m.AvailableLlamaBackends(context.Background())
	if err != nil || tag != "b10333" || len(ids) != 1 || ids[0] != "cpu" || current != "" {
		t.Fatalf("tag=%q ids=%v current=%q err=%v", tag, ids, current, err)
	}
	results, err := m.Check(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Latest != "b10333" || !results[0].Available || results[1].Latest != "v1.0.0" {
		t.Fatalf("results=%+v", results)
	}
}

func TestLlamaBuildPrereleaseUpgradeAndDowngradeChecks(t *testing.T) {
	for _, installed := range []string{"b10332", "b10333", "b10334"} {
		t.Run(installed, func(t *testing.T) {
			m, _ := newLlamaReleaseFixture(t)
			state := State{Schema: StateSchema, LlamaTag: installed, Backend: "cpu", ActiveRuntime: runtimeRelative("cpu", installed), Assets: []InstalledAsset{{Name: "runtime.tar.gz", SHA256: strings.Repeat("a", 64)}}}
			if err := SaveState(m.Layout, state); err != nil {
				t.Fatal(err)
			}
			results, err := m.Check(context.Background(), "llama")
			if err != nil || len(results) != 1 || results[0].Available != (installed == "b10332") {
				t.Fatalf("results=%+v err=%v", results, err)
			}
			_, err = m.PrepareLlama(context.Background(), LlamaOptions{})
			switch installed {
			case "b10332":
				if err != nil {
					t.Fatal(err)
				}
			case "b10333":
				if !errors.Is(err, ErrAlreadyCurrent) {
					t.Fatalf("same build: %v", err)
				}
			case "b10334":
				if err == nil || !strings.Contains(err.Error(), "降级") {
					t.Fatalf("downgrade: %v", err)
				}
				if _, err := m.PrepareLlama(context.Background(), LlamaOptions{AllowDowngrade: true}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
