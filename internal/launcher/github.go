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
	HTTP            *http.Client
	DirectHTTP      *http.Client
	ProxyResolver   func(*http.Request) (*url.URL, error)
	APIBase         string
	Token           string
	DownloadLogRoot string
}

func NewGitHubClient() *GitHubClient {
	proxyTransport := http.DefaultTransport.(*http.Transport).Clone()
	// Explicitly retain the operating environment's proxy rules. This honors
	// HTTP_PROXY, HTTPS_PROXY and NO_PROXY, including their lowercase forms.
	proxyTransport.Proxy = http.ProxyFromEnvironment
	directTransport := http.DefaultTransport.(*http.Transport).Clone()
	directTransport.Proxy = nil
	redirectPolicy := func(request *http.Request, via []*http.Request) error {
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
	}
	return &GitHubClient{
		HTTP:          &http.Client{Transport: proxyTransport, Timeout: 0, CheckRedirect: redirectPolicy},
		DirectHTTP:    &http.Client{Transport: directTransport, Timeout: 0, CheckRedirect: redirectPolicy},
		ProxyResolver: http.ProxyFromEnvironment,
		APIBase:       githubAPIBase,
		Token:         strings.TrimSpace(os.Getenv("LLAMALC_GITHUB_TOKEN")),
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
	req, err := client.newAPIRequest(ctx, endpoint, parsed)
	if err != nil {
		return GitHubRelease{}, err
	}
	release, requestErr := client.releaseAttempt(req, tag, client.httpClient())
	if requestErr == nil || ctx.Err() != nil || !client.usesSystemProxy(req) || client.directHTTPClient() == nil {
		return release, requestErr
	}
	directRequest, err := client.newAPIRequest(ctx, endpoint, parsed)
	if err != nil {
		return GitHubRelease{}, err
	}
	release, directErr := client.releaseAttempt(directRequest, tag, client.directHTTPClient())
	if directErr != nil {
		return GitHubRelease{}, fmt.Errorf("GitHub API 经系统代理失败（%v），直连重试也失败: %w", requestErr, directErr)
	}
	return release, nil
}

func (client *GitHubClient) newAPIRequest(ctx context.Context, endpoint string, parsed *url.URL) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "LlamaLc-update-client")
	// Never leak the token to redirects or download hosts.
	if strings.EqualFold(parsed.Hostname(), "api.github.com") && client.Token != "" {
		req.Header.Set("Authorization", "Bearer "+client.Token)
	}
	return req, nil
}

func (client *GitHubClient) releaseAttempt(req *http.Request, tag string, httpClient *http.Client) (GitHubRelease, error) {
	response, err := httpClient.Do(req)
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

func (client *GitHubClient) directHTTPClient() *http.Client {
	if client.DirectHTTP != nil {
		return client.DirectHTTP
	}
	return nil
}

func (client *GitHubClient) usesSystemProxy(request *http.Request) bool {
	resolver := client.ProxyResolver
	if resolver == nil {
		resolver = http.ProxyFromEnvironment
	}
	proxyURL, err := resolver(request)
	return err != nil || proxyURL != nil
}

type progressWriter struct {
	writer   io.Writer
	out      io.Writer
	name     string
	total    int64
	done     int64
	last     time.Time
	start    time.Time
	shown    bool
	doneLine bool
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	n, err := writer.writer.Write(data)
	writer.done += int64(n)
	if writer.out != nil && (time.Since(writer.last) >= 200*time.Millisecond || writer.done == writer.total) {
		writer.render()
		writer.last = time.Now()
		if writer.done == writer.total {
			fmt.Fprintln(writer.out)
			writer.doneLine = true
		}
	}
	return n, err
}

func (writer *progressWriter) render() {
	const width = 30
	percent, filled := int64(0), 0
	if writer.total > 0 {
		percent = writer.done * 100 / writer.total
		filled = int(writer.done * width / writer.total)
		if filled > width {
			filled = width
		}
	}
	bar := strings.Repeat("=", filled)
	if filled < width {
		bar += ">" + strings.Repeat("-", width-filled-1)
	}
	elapsed := time.Since(writer.start).Seconds()
	speed := float64(0)
	if elapsed > 0 {
		speed = float64(writer.done) / elapsed
	}
	fmt.Fprintf(writer.out, "\r下载 %s [%s] %3d%% %s/%s %s/s", writer.name, bar, percent, humanBytes(float64(writer.done)), humanBytes(float64(writer.total)), humanBytes(speed))
	writer.shown = true
}

func (writer *progressWriter) finishFailure() {
	if writer.out != nil && writer.shown && !writer.doneLine {
		fmt.Fprintln(writer.out)
		writer.doneLine = true
	}
}

func humanBytes(value float64) string {
	units := []string{"B", "KiB", "MiB", "GiB"}
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
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
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("下载目标已存在: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	req, err := newDownloadRequest(ctx, parsed.String())
	if err != nil {
		return "", err
	}
	usesProxy := client.usesSystemProxy(req)
	route := "直连"
	if usesProxy {
		route = "系统代理"
	}
	if out != nil {
		fmt.Fprintf(out, "下载开始: %s（%s，%s）\n", asset.Name, humanBytes(float64(asset.Size)), route)
	}
	client.logDownload(out, fmt.Sprintf("START asset=%q size=%d route=%s host=%q destination=%q", asset.Name, asset.Size, route, parsed.Hostname(), destination))
	digest, requestErr, retryable := client.downloadAttempt(req, asset, destination, expected, out, client.httpClient())
	if requestErr == nil || !retryable || ctx.Err() != nil || !usesProxy || client.directHTTPClient() == nil {
		if requestErr == nil {
			client.logDownload(out, fmt.Sprintf("COMPLETE asset=%q size=%d route=%s sha256=%s", asset.Name, asset.Size, route, digest))
			if out != nil {
				fmt.Fprintf(out, "下载完成: %s（SHA-256: %s）\n", asset.Name, digest[:12]+"…")
			}
		} else {
			client.logDownload(out, fmt.Sprintf("FAILED asset=%q route=%s error=%q", asset.Name, route, requestErr))
		}
		return digest, requestErr
	}
	if out != nil {
		fmt.Fprintf(out, "系统代理下载失败，正在直连重试 %s……\n", asset.Name)
	}
	client.logDownload(out, fmt.Sprintf("RETRY_DIRECT asset=%q proxy_error=%q", asset.Name, requestErr))
	directRequest, err := newDownloadRequest(ctx, parsed.String())
	if err != nil {
		return "", err
	}
	digest, directErr, _ := client.downloadAttempt(directRequest, asset, destination, expected, out, client.directHTTPClient())
	if directErr != nil {
		client.logDownload(out, fmt.Sprintf("FAILED asset=%q route=直连 proxy_error=%q direct_error=%q", asset.Name, requestErr, directErr))
		return "", fmt.Errorf("下载 %s 经系统代理失败（%v），直连重试也失败: %w", asset.Name, requestErr, directErr)
	}
	client.logDownload(out, fmt.Sprintf("COMPLETE asset=%q size=%d route=直连 sha256=%s", asset.Name, asset.Size, digest))
	if out != nil {
		fmt.Fprintf(out, "下载完成: %s（SHA-256: %s）\n", asset.Name, digest[:12]+"…")
	}
	return digest, nil
}

func (client *GitHubClient) logDownload(out io.Writer, event string) {
	if err := appendDownloadLog(client.DownloadLogRoot, event); err != nil && out != nil {
		fmt.Fprintf(out, "警告: %v\n", err)
	}
}

func newDownloadRequest(ctx context.Context, endpoint string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "LlamaLc-update-client")
	return req, nil
}

func (client *GitHubClient) downloadAttempt(req *http.Request, asset GitHubAsset, destination, expected string, out io.Writer, httpClient *http.Client) (string, error, bool) {
	response, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败: %w", asset.Name, err), true
	}
	defer response.Body.Close()
	if response.Request == nil || response.Request.URL.Scheme != "https" {
		return "", fmt.Errorf("资产 %s 最终响应不是 HTTPS", asset.Name), true
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 返回 %s", asset.Name, response.Status), true
	}
	if response.ContentLength > maxAssetDownload || (response.ContentLength >= 0 && response.ContentLength != asset.Size) {
		return "", fmt.Errorf("资产 %s Content-Length 与 API 不一致", asset.Name), true
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err, false
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
	progress := &progressWriter{writer: io.MultiWriter(file, hash), out: out, name: asset.Name, total: asset.Size, start: time.Now()}
	written, copyErr := io.Copy(progress, limited)
	if copyErr != nil {
		progress.finishFailure()
		return "", fmt.Errorf("下载 %s 中断: %w", asset.Name, copyErr), true
	}
	if written != asset.Size || limited.N <= 0 {
		progress.finishFailure()
		return "", fmt.Errorf("资产 %s 下载字节数不符: %d != %d", asset.Name, written, asset.Size), true
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return "", fmt.Errorf("资产 %s SHA-256 不匹配", asset.Name), true
	}
	if err := file.Sync(); err != nil {
		return "", err, false
	}
	if err := file.Close(); err != nil {
		return "", err, false
	}
	remove = false
	return actual, nil, false
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
