package launcher

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type BackendOption struct {
	ID     string
	Label  string
	Assets []GitHubAsset
}

var llamaAssetPattern = regexp.MustCompile(`(?i)^llama-b[0-9]+-bin-(win|ubuntu)-(.+)\.(zip|tar\.gz)$`)

func ResolveLlamaAssets(release GitHubRelease, goos, goarch string) ([]BackendOption, error) {
	platform := map[string]string{"windows": "win", "linux": "ubuntu"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if platform == "" || arch == "" {
		return nil, fmt.Errorf("当前平台 %s/%s 没有受支持的 llama.cpp Release", goos, goarch)
	}
	options := make(map[string]*BackendOption)
	for _, asset := range release.Assets {
		match := llamaAssetPattern.FindStringSubmatch(asset.Name)
		if len(match) == 0 || !strings.EqualFold(match[1], platform) {
			continue
		}
		middle := strings.ToLower(match[2])
		if middle != arch && !strings.HasSuffix(middle, "-"+arch) {
			continue
		}
		backend := strings.TrimSuffix(middle, "-"+arch)
		if middle == arch || backend == "" || backend == "cpu" {
			backend = "cpu"
		}
		if strings.Contains(backend, "source") || strings.Contains(backend, "android") || strings.Contains(backend, "server") {
			continue
		}
		if _, err := digestHex(asset.Digest); err != nil {
			return nil, fmt.Errorf("候选资产 %s 没有 SHA-256 digest", asset.Name)
		}
		option := options[backend]
		if option == nil {
			option = &BackendOption{ID: backend, Label: backend}
			options[backend] = option
		}
		option.Assets = append(option.Assets, asset)
	}
	for id, option := range options {
		if strings.HasPrefix(id, "cuda-") && goos == "windows" {
			companionPrefix := "cudart-llama-bin-win-" + id + "-" + arch
			var companion *GitHubAsset
			for index := range release.Assets {
				asset := &release.Assets[index]
				stem := strings.TrimSuffix(strings.ToLower(asset.Name), filepath.Ext(asset.Name))
				if strings.HasSuffix(strings.ToLower(asset.Name), ".tar.gz") {
					stem = strings.TrimSuffix(strings.ToLower(asset.Name), ".tar.gz")
				}
				if stem == companionPrefix {
					if _, err := digestHex(asset.Digest); err != nil {
						return nil, fmt.Errorf("CUDA 伴随资产 %s 没有 SHA-256 digest", asset.Name)
					}
					copy := *asset
					companion = &copy
					break
				}
			}
			if companion == nil {
				delete(options, id)
				continue
			}
			option.Assets = append(option.Assets, *companion)
		}
	}
	result := make([]BackendOption, 0, len(options))
	for _, option := range options {
		sort.Slice(option.Assets, func(i, j int) bool { return option.Assets[i].Name < option.Assets[j].Name })
		result = append(result, *option)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ID == "cpu" {
			return true
		}
		if result[j].ID == "cpu" {
			return false
		}
		return result[i].ID < result[j].ID
	})
	if len(result) == 0 {
		return nil, fmt.Errorf("Release %s 没有适用于 %s/%s 的完整资产组合", release.TagName, goos, goarch)
	}
	return result, nil
}

func SelectBackend(options []BackendOption, id string) (BackendOption, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, option := range options {
		if option.ID == id {
			return option, nil
		}
	}
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.ID)
	}
	if id == "" {
		return BackendOption{}, fmt.Errorf("必须使用 --backend 指定后端；可用值: %s", strings.Join(values, ", "))
	}
	return BackendOption{}, fmt.Errorf("后端 %q 在当前 Release 中不可用；可用值: %s", id, strings.Join(values, ", "))
}

func LlamaBuildNumber(tag string) (int64, error) {
	tag = strings.TrimSpace(strings.ToLower(tag))
	if len(tag) < 2 || tag[0] != 'b' {
		return 0, fmt.Errorf("llama.cpp tag 不是 b<数字>: %q", tag)
	}
	var number int64
	for _, char := range tag[1:] {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("llama.cpp tag 不是 b<数字>: %q", tag)
		}
		number = number*10 + int64(char-'0')
	}
	return number, nil
}

func CompareLlamaTags(a, b string) (int, error) {
	aNumber, err := LlamaBuildNumber(a)
	if err != nil {
		return 0, err
	}
	bNumber, err := LlamaBuildNumber(b)
	if err != nil {
		return 0, err
	}
	switch {
	case aNumber < bNumber:
		return -1, nil
	case aNumber > bNumber:
		return 1, nil
	default:
		return 0, nil
	}
}

type semVersion struct{ major, minor, patch int64 }

func parseSemVersion(value string) (semVersion, error) {
	value = strings.TrimPrefix(strings.TrimSpace(strings.ToLower(value)), "v")
	core := strings.SplitN(value, "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semVersion{}, errors.New("不是 SemVer")
	}
	var result semVersion
	values := []*int64{&result.major, &result.minor, &result.patch}
	for index, part := range parts {
		if part == "" {
			return semVersion{}, errors.New("不是 SemVer")
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return semVersion{}, errors.New("不是 SemVer")
			}
			*values[index] = *values[index]*10 + int64(char-'0')
		}
	}
	return result, nil
}

func CompareSemVer(a, b string) (int, error) {
	av, err := parseSemVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseSemVersion(b)
	if err != nil {
		return 0, err
	}
	left := []int64{av.major, av.minor, av.patch}
	right := []int64{bv.major, bv.minor, bv.patch}
	for index := range left {
		if left[index] < right[index] {
			return -1, nil
		}
		if left[index] > right[index] {
			return 1, nil
		}
	}
	return 0, nil
}
