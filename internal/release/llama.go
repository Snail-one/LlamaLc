package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const LlamaRepository = "ggml-org/llama.cpp"

const (
	ChannelPreview = "preview"
	ChannelStable  = "stable"
)

var llamaBuildTag = regexp.MustCompile(`(?i)^b[0-9]+$`)
var llamaStableTag = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func NormalizeLlamaChannel(channel string) (string, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" {
		channel = ChannelPreview
	}
	if channel != ChannelPreview && channel != ChannelStable {
		return "", fmt.Errorf("llama.cpp 更新通道必须为 preview 或 stable: %q", channel)
	}
	return channel, nil
}

// LatestLlama defaults to the latest binary build, including prereleases.
// Stable follows GitHub's latest non-prerelease and resolves its build pointer.
func (c *Client) LatestLlama(ctx context.Context, channel string) (GitHubRelease, error) {
	channel, err := NormalizeLlamaChannel(channel)
	if err != nil {
		return GitHubRelease{}, err
	}
	if channel == ChannelPreview {
		return c.latestLlamaBuild(ctx)
	}
	rel, err := c.fetchRelease(ctx, LlamaRepository, "")
	if err != nil {
		return GitHubRelease{}, err
	}
	return c.resolveLlamaRelease(ctx, rel)
}

// Semantic releases currently carry nightly-tag.txt instead of binaries.
// Keep the actual build tag for version probes and safe downgrade comparison.
func (c *Client) resolveLlamaRelease(ctx context.Context, rel GitHubRelease) (GitHubRelease, error) {
	if llamaBuildTag.MatchString(rel.Tag) {
		return rel, nil
	}
	if !llamaStableTag.MatchString(rel.Tag) {
		return GitHubRelease{}, fmt.Errorf("不支持的 llama.cpp Release tag: %q", rel.Tag)
	}
	var pointer Asset
	for _, asset := range rel.Assets {
		if asset.Name == "nightly-tag.txt" {
			pointer = asset
			break
		}
	}
	if pointer.Name == "" {
		return GitHubRelease{}, fmt.Errorf("llama.cpp %s 缺少 nightly-tag.txt，无法确定对应构建", rel.Tag)
	}
	const maxPointerSize = 128
	if pointer.Size <= 0 || pointer.Size > maxPointerSize {
		return GitHubRelease{}, errors.New("nightly-tag.txt 大小无效或超过 128 字节")
	}
	directory, err := os.MkdirTemp("", "llamalc-release-*")
	if err != nil {
		return GitHubRelease{}, err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, pointer.Name)
	// Metadata resolution must keep JSON checks and preflight output clean.
	metadataClient := *c
	metadataClient.Progress = nil
	if err := metadataClient.Download(ctx, pointer, path); err != nil {
		return GitHubRelease{}, fmt.Errorf("解析 llama.cpp %s 的 nightly-tag.txt: %w", rel.Tag, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return GitHubRelease{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxPointerSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return GitHubRelease{}, readErr
	}
	if closeErr != nil {
		return GitHubRelease{}, closeErr
	}
	tag := strings.TrimSpace(string(data))
	if int64(len(data)) != pointer.Size || !llamaBuildTag.MatchString(tag) {
		return GitHubRelease{}, fmt.Errorf("llama.cpp %s 的 nightly-tag.txt 不是有效 b<数字> 构建", rel.Tag)
	}
	build, err := c.fetchRelease(ctx, LlamaRepository, tag)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("获取 llama.cpp %s 对应构建 %s: %w", rel.Tag, tag, err)
	}
	build.ReleaseTag = rel.Tag
	return build, nil
}

// latestLlamaBuild uses the release list because upstream marks b-numbered
// binary builds as prereleases, which GitHub's /releases/latest excludes.
func (c *Client) latestLlamaBuild(ctx context.Context) (GitHubRelease, error) {
	const perPage = 20
	const maxPages = 5
	for page := 1; page <= maxPages; page++ {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d&page=%d", LlamaRepository, perPage, page)
		data, err := c.fetchReleaseData(ctx, endpoint)
		if err != nil {
			return GitHubRelease{}, err
		}
		var releases []GitHubRelease
		if err := decodeReleaseData(data, &releases); err != nil {
			return GitHubRelease{}, err
		}
		for _, r := range releases {
			if r.Draft || !llamaBuildTag.MatchString(r.Tag) {
				continue
			}
			if err := validateRelease(r, true); err != nil {
				return GitHubRelease{}, err
			}
			return r, nil
		}
		if len(releases) < perPage {
			break
		}
	}
	return GitHubRelease{}, errors.New("GitHub Releases 列表中未找到 llama.cpp b<数字> 构建（最多检查 100 条）")
}
