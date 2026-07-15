package launcher

import (
	"bufio"
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
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIBase      = "https://api.github.com"
	maxAPIResponseSize = 4 << 20
	maxAssetDownload   = int64(2 << 30)
	maxTotalDownload   = int64(4 << 30)
	launcherRepository = "Snail-one/LlamaLc"
	llamaRepository    = "ggml-org/llama.cpp"
)

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
}

type GitHubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []GitHubAsset `json:"assets"`
}

type GitHubClient struct {
	HTTP    *http.Client
	APIBase string
	Token   string
}

func NewGitHubClient() *GitHubClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Explicitly retain the operating environment's proxy rules. This honors
	// HTTP_PROXY, HTTPS_PROXY and NO_PROXY, including their lowercase forms.
	transport.Proxy = http.ProxyFromEnvironment
	return &GitHubClient{
		HTTP: &http.Client{Transport: transport, Timeout: 0, CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" {
				return errors.New("拒绝重定向到非 HTTPS URL")
			}
			if !strings.EqualFold(request.URL.Hostname(), "api.github.com") {
				request.Header.Del("Authorization")
			}
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			return nil
		}},
		APIBase: githubAPIBase,
		Token:   strings.TrimSpace(os.Getenv("LLAMALC_GITHUB_TOKEN")),
	}
}

func (client *GitHubClient) Release(ctx context.Context, repository, tag string) (GitHubRelease, error) {
	if client == nil {
		return GitHubRelease{}, errors.New("GitHub 客户端为空")
	}
	base := strings.TrimRight(client.APIBase, "/")
	endpoint := base + "/repos/" + repository + "/releases/latest"
	if strings.TrimSpace(tag) != "" {
		endpoint = base + "/repos/" + repository + "/releases/tags/" + url.PathEscape(tag)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" {
		return GitHubRelease{}, fmt.Errorf("GitHub API 仅允许 HTTPS: %s", endpoint)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GitHubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "LlamaLc-update-client")
	// Never leak the token to redirects or download hosts.
	if strings.EqualFold(parsed.Hostname(), "api.github.com") && client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	response, err := client.httpClient().Do(req)
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("GitHub API 请求失败: %w", err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		return GitHubRelease{}, errors.New("GitHub API 最终响应不是 HTTPS")
	}
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		remaining := response.Header.Get("X-RateLimit-Remaining")
		reset := response.Header.Get("X-RateLimit-Reset")
		if response.StatusCode == http.StatusForbidden && remaining == "0" {
			return GitHubRelease{}, fmt.Errorf("GitHub API 限流（reset=%s）", reset)
		}
		return GitHubRelease{}, fmt.Errorf("GitHub API 返回 %s: %s", response.Status, safeTerminalText(string(message)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseSize+1))
	if err != nil {
		return GitHubRelease{}, fmt.Errorf("读取 GitHub API 响应失败: %w", err)
	}
	if len(data) > maxAPIResponseSize {
		return GitHubRelease{}, fmt.Errorf("GitHub API 响应超过 %d 字节", maxAPIResponseSize)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var release GitHubRelease
	if err := decoder.Decode(&release); err != nil {
		return GitHubRelease{}, fmt.Errorf("无法解析 GitHub Release: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return GitHubRelease{}, errors.New("GitHub Release 响应包含多余 JSON 内容")
	}
	if strings.TrimSpace(release.TagName) == "" || release.Draft || (tag == "" && release.Prerelease) {
		return GitHubRelease{}, errors.New("GitHub Release 不是可用的稳定版本")
	}
	return release, nil
}

func (client *GitHubClient) httpClient() *http.Client {
	if client.HTTP != nil {
		return client.HTTP
	}
	return NewGitHubClient().HTTP
}

type progressWriter struct {
	writer io.Writer
	out    io.Writer
	name   string
	total  int64
	done   int64
	last   time.Time
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	n, err := writer.writer.Write(data)
	writer.done += int64(n)
	if writer.out != nil && (time.Since(writer.last) >= 200*time.Millisecond || writer.done == writer.total) {
		percent := "?"
		if writer.total > 0 {
			percent = strconv.FormatInt(writer.done*100/writer.total, 10)
		}
		fmt.Fprintf(writer.out, "\r下载 %s: %d/%d 字节 (%s%%)", writer.name, writer.done, writer.total, percent)
		writer.last = time.Now()
		if writer.done == writer.total {
			fmt.Fprintln(writer.out)
		}
	}
	return n, err
}

func (client *GitHubClient) Download(ctx context.Context, asset GitHubAsset, destination string, out io.Writer) (string, error) {
	if asset.Size <= 0 || asset.Size > maxAssetDownload {
		return "", fmt.Errorf("资产 %s 大小无效或超过 2 GiB: %d", asset.Name, asset.Size)
	}
	expected, err := digestHex(asset.Digest)
	if err != nil {
		return "", fmt.Errorf("资产 %s 缺少有效 SHA-256 digest: %w", asset.Name, err)
	}
	parsed, err := url.Parse(asset.BrowserDownloadURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("资产下载仅允许 HTTPS: %s", asset.BrowserDownloadURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "LlamaLc-update-client")
	response, err := client.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		return "", fmt.Errorf("资产 %s 最终响应不是 HTTPS", asset.Name)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 返回 %s", asset.Name, response.Status)
	}
	if response.ContentLength > maxAssetDownload || (response.ContentLength >= 0 && response.ContentLength != asset.Size) {
		return "", fmt.Errorf("资产 %s Content-Length 与 API 不一致", asset.Name)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	remove := true
	defer func() {
		file.Close()
		if remove {
			os.Remove(destination)
		}
	}()
	hash := sha256.New()
	limited := &io.LimitedReader{R: response.Body, N: maxAssetDownload + 1}
	progress := &progressWriter{writer: io.MultiWriter(file, hash), out: out, name: asset.Name, total: asset.Size}
	written, copyErr := io.Copy(progress, limited)
	if copyErr != nil {
		return "", fmt.Errorf("下载 %s 中断: %w", asset.Name, copyErr)
	}
	if written != asset.Size || limited.N <= 0 {
		return "", fmt.Errorf("资产 %s 下载字节数不符: %d != %d", asset.Name, written, asset.Size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return "", fmt.Errorf("资产 %s SHA-256 不匹配", asset.Name)
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	remove = false
	return actual, nil
}

func digestHex(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sha256:") {
		value = value[len("sha256:"):]
	}
	if !validSHA256(value) {
		return "", errors.New("不是 64 位十六进制 SHA-256")
	}
	return strings.ToLower(value), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func parseSHA256SUMS(data []byte) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !validSHA256(fields[0]) {
			return nil, fmt.Errorf("SHA256SUMS.txt 格式无效")
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" || strings.ContainsAny(name, "/\\") {
			return nil, fmt.Errorf("SHA256SUMS.txt 含无效文件名")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("SHA256SUMS.txt 含重复条目 %s", name)
		}
		result[name] = strings.ToLower(fields[0])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
