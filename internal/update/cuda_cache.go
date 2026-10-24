package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Snail-one/LlamaLc/internal/managedfs"
	"github.com/Snail-one/LlamaLc/internal/release"
)

var cudaRuntimeAssetName = regexp.MustCompile(`(?i)^cudart-llama-bin-win-cuda-[0-9]+\.[0-9]+-(x64|arm64)\.zip$`)

func (m *Manager) cudaCachePath(asset release.Asset) (string, error) {
	if !cudaRuntimeAssetName.MatchString(asset.Name) {
		return "", errors.New("无效的 CUDA 运行库资产名")
	}
	if asset.Size <= 0 || asset.Size > 2<<30 {
		return "", errors.New("CUDA 运行库资产大小无效或超过 2 GiB")
	}
	digest, err := release.Digest(asset.Digest)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.Layout.DownloadCacheDir, "cuda", digest+".zip"), nil
}

// validCUDAArchive checks bytes every time, including a cache hit. Names and
// previously recorded digests alone cannot establish that cached data is valid.
func (m *Manager) validCUDAArchive(path string, asset release.Asset) (bool, error) {
	if err := managedfs.Validate(m.Layout.Root, path, true); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("CUDA 运行库缓存必须是普通文件: %s", path)
	}
	if info.Size() != asset.Size {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, opened) {
		return false, errors.New("CUDA 运行库缓存文件身份发生变化")
	}
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, asset.Size+1))
	if err != nil {
		return false, err
	}
	expected, err := release.Digest(asset.Digest)
	if err != nil {
		return false, err
	}
	return size == asset.Size && hex.EncodeToString(digest.Sum(nil)) == expected, nil
}

// cudaRuntimeArchive is called under the global llama install lock. Downloads
// stay in owned staging until verified, then move atomically into the cache.
// The cache lives outside replaceable runtimes and survives failed installs.
func (m *Manager) cudaRuntimeArchive(ctx context.Context, asset release.Asset, staging string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := m.cudaCachePath(asset)
	if err != nil {
		return "", err
	}
	valid, err := m.validCUDAArchive(path, asset)
	if err != nil {
		return "", err
	}
	if valid {
		if m.Out != nil {
			fmt.Fprintln(m.Out, "复用已校验的 CUDA 运行库缓存:", asset.Name)
		}
		return path, nil
	}
	if err := managedfs.EnsureDir(m.Layout.Root, filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	archive := filepath.Join(staging, asset.Name)
	if err := m.Source.Download(ctx, asset, archive); err != nil {
		return "", err
	}
	valid, err = m.validCUDAArchive(archive, asset)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", fmt.Errorf("CUDA 运行库 %s 下载大小或 SHA-256 校验失败", asset.Name)
	}
	if err := managedfs.Validate(m.Layout.Root, path, true); err != nil {
		return "", err
	}
	if err := os.Rename(archive, path); err != nil {
		return "", fmt.Errorf("保存 CUDA 运行库缓存: %w", err)
	}
	return path, nil
}
