// Package release talks to GitHub Releases and safely downloads and extracts assets.
package release

import (
	"archive/tar"
	"archive/zip"
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
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Snail-one/LlamaLc/internal/managedfs"
)

const DefaultProxy = "https://ghfast.top/"
const maxExtractedSize int64 = 32 << 30

type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}
type GitHubRelease struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}
type Client struct {
	HTTP          *http.Client
	DirectHTTP    *http.Client
	ProxyResolver func(*http.Request) (*url.URL, error)
	Proxy         string
	Token         string
}

func NewClient(proxy string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	resolver := newSystemProxyResolver()
	transport.Proxy = resolver
	redirect := func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return errors.New("拒绝重定向到非 HTTPS 下载地址")
		}
		if len(via) > 10 {
			return errors.New("下载重定向过多")
		}
		return nil
	}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: redirect}
	directTransport := http.DefaultTransport.(*http.Transport).Clone()
	directTransport.Proxy = nil
	return &Client{HTTP: client, DirectHTTP: &http.Client{Transport: directTransport, Timeout: 2 * time.Minute, CheckRedirect: redirect}, ProxyResolver: resolver, Proxy: strings.TrimSpace(proxy), Token: strings.TrimSpace(os.Getenv("LLAMALC_GITHUB_TOKEN"))}
}
func (c *Client) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	retryable := err != nil || (resp != nil && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout))
	if !retryable || c.DirectHTTP == nil || c.ProxyResolver == nil {
		return resp, err
	}
	proxy, proxyErr := c.ProxyResolver(req)
	if proxyErr == nil && proxy == nil {
		return resp, err
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	return c.DirectHTTP.Do(req.Clone(req.Context()))
}
func (c *Client) Latest(ctx context.Context, repo string) (GitHubRelease, error) {
	endpoint := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GitHubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "LlamaLc")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.do(req)
	if err != nil {
		return GitHubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return GitHubRelease{}, fmt.Errorf("GitHub Release 请求失败: %s", resp.Status)
	}
	var r GitHubRelease
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return r, err
	}
	if r.Tag == "" {
		return r, errors.New("GitHub Release 缺少 tag")
	}
	return r, nil
}
func (c *Client) Download(ctx context.Context, a Asset, destination string) error {
	expected, err := Digest(a.Digest)
	if err != nil {
		return err
	}
	downloadURL := a.URL
	parsedAsset, err := url.Parse(a.URL)
	if err != nil || parsedAsset.Scheme != "https" || parsedAsset.Host == "" {
		return errors.New("资产下载地址必须是完整 HTTPS URL")
	}
	if c.Proxy != "" {
		parsedProxy, parseErr := url.Parse(c.Proxy)
		if parseErr != nil || parsedProxy.Scheme != "https" || parsedProxy.Host == "" {
			return fmt.Errorf("代理 URL 无效: %q", c.Proxy)
		}
		downloadURL = strings.TrimRight(c.Proxy, "/") + "/" + a.URL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("下载 %s: %s", a.Name, resp.Status)
	}
	if resp.Request.URL.Scheme != "https" {
		return errors.New("资产响应来自非 HTTPS 地址")
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxDownload(a.Size)))
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("下载大小不符: 预期 %d，实际 %d", a.Size, n)
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("SHA-256 不匹配: 预期 %s，实际 %s", expected, actual)
	}
	return nil
}

func SHA256SumsAsset(r GitHubRelease) (Asset, error) {
	for _, a := range r.Assets {
		if a.Name == "SHA256SUMS.txt" {
			if _, err := Digest(a.Digest); err != nil {
				return Asset{}, err
			}
			return a, nil
		}
	}
	return Asset{}, errors.New("Release 缺少 SHA256SUMS.txt")
}
func ParseSHA256Sums(data []byte) (map[string]string, error) {
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			return nil, errors.New("SHA256SUMS.txt 格式无效")
		}
		digest, err := Digest(fields[0])
		if err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(name) != name || name == "" {
			return nil, errors.New("SHA256SUMS.txt 包含无效文件名")
		}
		if _, exists := result[name]; exists {
			return nil, errors.New("SHA256SUMS.txt 包含重复文件")
		}
		result[name] = digest
	}
	if len(result) == 0 {
		return nil, errors.New("SHA256SUMS.txt 为空")
	}
	return result, nil
}
func maxDownload(size int64) int64 {
	if size > 0 {
		return size + 1
	}
	return 8 << 30
}
func Digest(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return "", errors.New("资产缺少有效 SHA-256 digest")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("资产 SHA-256 digest 无效")
	}
	return value, nil
}

type Backend struct {
	ID     string
	Assets []Asset
}

var llamaAsset = regexp.MustCompile(`(?i)^llama-b[0-9]+-bin-(win|ubuntu)-(.+)\.(zip|tar\.gz)$`)

func LlamaAssets(r GitHubRelease, goos, goarch string) ([]Backend, error) {
	platform := map[string]string{"windows": "win", "linux": "ubuntu"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if platform == "" || arch == "" {
		return nil, errors.New("平台不受支持")
	}
	m := map[string][]Asset{}
	for _, a := range r.Assets {
		x := llamaAsset.FindStringSubmatch(a.Name)
		if len(x) == 0 || !strings.EqualFold(x[1], platform) {
			continue
		}
		middle := strings.ToLower(x[2])
		if middle != arch && !strings.HasSuffix(middle, "-"+arch) {
			continue
		}
		id := strings.TrimSuffix(middle, "-"+arch)
		if id == "" || id == arch || id == "cpu" {
			id = "cpu"
		}
		if _, err := Digest(a.Digest); err != nil {
			return nil, fmt.Errorf("%s: %w", a.Name, err)
		}
		m[id] = append(m[id], a)
	}
	var out []Backend
	for id, assets := range m {
		if strings.HasPrefix(id, "cuda-") && goos == "windows" {
			prefix := "cudart-llama-bin-win-" + id + "-" + arch
			var companion *Asset
			for i := range r.Assets {
				stem := strings.TrimSuffix(strings.ToLower(r.Assets[i].Name), ".zip")
				if stem == prefix {
					copy := r.Assets[i]
					companion = &copy
					break
				}
			}
			if companion == nil {
				continue
			}
			if _, err := Digest(companion.Digest); err != nil {
				return nil, fmt.Errorf("%s: %w", companion.Name, err)
			}
			assets = append(assets, *companion)
		}
		sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
		out = append(out, Backend{ID: id, Assets: assets})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == "cpu" {
			return true
		}
		if out[j].ID == "cpu" {
			return false
		}
		return out[i].ID < out[j].ID
	})
	if len(out) == 0 {
		return nil, errors.New("Release 没有当前平台资产")
	}
	return out, nil
}
func LauncherAsset(r GitHubRelease, goos, goarch string) (Asset, error) {
	name := "llamalc-" + goos + "-" + goarch + "-" + r.Tag
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	for _, a := range r.Assets {
		if a.Name == name+ext {
			if _, err := Digest(a.Digest); err != nil {
				return Asset{}, err
			}
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("Release %s 缺少 %s", r.Tag, name+ext)
}

func Extract(archive, destination string) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	lower := strings.ToLower(archive)
	if strings.HasSuffix(lower, ".zip") {
		return extractZip(archive, destination)
	}
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") {
		return extractTar(archive, destination)
	}
	return errors.New("不支持的归档格式")
}
func safeTarget(root, name string) (string, error) {
	name = filepath.FromSlash(name)
	if filepath.IsAbs(name) {
		return "", errors.New("归档包含绝对路径")
	}
	target := filepath.Join(root, filepath.Clean(name))
	if err := managedfs.Within(root, target); err != nil {
		return "", err
	}
	return target, nil
}
func extractZip(path, root string) error {
	z, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer z.Close()
	var total int64
	for _, f := range z.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return errors.New("归档不允许符号链接")
		}
		target, err := safeTarget(root, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err = os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return errors.New("归档包含特殊文件")
		}
		if f.UncompressedSize64 > uint64(maxExtractedSize) || total > maxExtractedSize-int64(f.UncompressedSize64) {
			return errors.New("归档解压后过大")
		}
		total += int64(f.UncompressedSize64)
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.Mode().Perm())
		if err != nil {
			src.Close()
			return err
		}
		_, e1 := io.Copy(dst, src)
		e2 := dst.Close()
		e3 := src.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		if e3 != nil {
			return e3
		}
	}
	return nil
}
func extractTar(path, root string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeTarget(root, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0o700)
		case tar.TypeReg, tar.TypeRegA:
			if h.Size < 0 || h.Size > maxExtractedSize || total > maxExtractedSize-h.Size {
				return errors.New("归档解压后过大")
			}
			total += h.Size
			if err = os.MkdirAll(filepath.Dir(target), 0o700); err == nil {
				var out *os.File
				out, err = os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(h.Mode).Perm())
				if err == nil {
					_, err = io.Copy(out, tr)
					if closeErr := out.Close(); err == nil {
						err = closeErr
					}
				}
			}
		default:
			return errors.New("归档不允许链接或特殊文件")
		}
		if err != nil {
			return err
		}
	}
}
