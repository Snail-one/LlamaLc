package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLatestLlamaUsesBuildListIncludingPrereleases(t *testing.T) {
	calls := 0
	client := &Client{Token: "test-token", HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/repos/"+LlamaRepository+"/releases" || req.URL.Query().Get("page") != "1" || req.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected API request: %s headers=%v", req.URL, req.Header)
		}
		body := `[{"tag_name":"v0.7.0"},{"tag_name":"b11847","draft":true},{"tag_name":"b-invalid"},{"tag_name":"b11846","prerelease":true},{"tag_name":"b11840"}]`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}}
	rel, err := client.Latest(context.Background(), LlamaRepository)
	if err != nil || rel.Tag != "b11846" || !rel.Prerelease || calls != 1 {
		t.Fatalf("release=%+v err=%v calls=%d", rel, err, calls)
	}
}

func TestLlamaChannelValidationAndStableBuildRelease(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"", ChannelPreview}, {"preview", ChannelPreview}, {" stable ", ChannelStable}, {"PREVIEW", ChannelPreview}} {
		got, err := NormalizeLlamaChannel(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("channel=%q got=%q err=%v", tc.input, got, err)
		}
	}
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/repos/"+LlamaRepository+"/releases/latest" {
			t.Fatalf("unexpected stable URL: %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tag_name":"b12000"}`)), Request: req}, nil
	})}}
	if _, err := client.LatestLlama(context.Background(), "invalid"); err == nil {
		t.Fatal("accepted invalid channel")
	}
	rel, err := client.LatestLlama(context.Background(), ChannelStable)
	if err != nil || rel.Tag != "b12000" {
		t.Fatalf("release=%+v err=%v", rel, err)
	}
}

func TestLatestLlamaPaginatesAndBoundsSearch(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			calls := 0
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Query().Get("page") != fmt.Sprint(calls) {
					t.Fatalf("page=%s calls=%d", req.URL.Query().Get("page"), calls)
				}
				releases := make([]GitHubRelease, 20)
				for i := range releases {
					releases[i].Tag = "v0.7.0"
				}
				if found && calls == 2 {
					releases[0] = GitHubRelease{Tag: "b11840", Prerelease: true}
				}
				body, _ := json.Marshal(releases)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
			})}}
			rel, err := client.Latest(context.Background(), LlamaRepository)
			if found {
				if err != nil || rel.Tag != "b11840" || calls != 2 {
					t.Fatalf("release=%+v err=%v calls=%d", rel, err, calls)
				}
			} else if err == nil || calls != 5 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestLatestLlamaRejectsInvalidResponses(t *testing.T) {
	for name, body := range map[string]string{
		"empty":      `[]`,
		"no-build":   `[{"tag_name":"v0.7.0"},{"tag_name":"b1","draft":true}]`,
		"object":     `{"tag_name":"b11840"}`,
		"trailing":   `[{"tag_name":"b11840"}] {}`,
		"oversize":   strings.Repeat(" ", int(maxReleaseResponse)+1),
		"asset-path": `[{"tag_name":"b11840","assets":[{"name":"../evil.zip"}]}]`,
		"duplicate":  `[{"tag_name":"b11840","assets":[{"name":"a.zip"},{"name":"A.zip"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}}
			if _, err := client.Latest(context.Background(), LlamaRepository); err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestExactPrereleaseAllowedOnlyForLlamaBuilds(t *testing.T) {
	for _, tc := range []struct {
		name, repo, tag, responseTag string
		draft, wantOK                bool
	}{
		{"llama-build", LlamaRepository, "b11840", "b11840", false, true},
		{"draft", LlamaRepository, "b11840", "b11840", true, false},
		{"wrong-tag", LlamaRepository, "b11840", "b11846", false, false},
		{"semantic", LlamaRepository, "v0.7.0", "v0.7.0", false, false},
		{"launcher", "Snail-one/LlamaLc", "v1.0.0", "v1.0.0", false, false},
		{"other-repo", "owner/repo", "b11840", "b11840", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/repos/"+tc.repo+"/releases/tags/"+tc.tag {
					t.Fatalf("unexpected URL: %s", req.URL)
				}
				body, _ := json.Marshal(GitHubRelease{Tag: tc.responseTag, Prerelease: true, Draft: tc.draft})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
			})}}
			_, err := client.Release(context.Background(), tc.repo, tc.tag)
			if (err == nil) != tc.wantOK {
				t.Fatalf("err=%v wantOK=%v", err, tc.wantOK)
			}
		})
	}
}
