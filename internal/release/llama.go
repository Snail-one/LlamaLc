package release

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

const LlamaRepository = "ggml-org/llama.cpp"

var llamaBuildTag = regexp.MustCompile(`(?i)^b[0-9]+$`)

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
