#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}"

if ! command -v go >/dev/null 2>&1; then
    echo "错误: 找不到 Go，请先安装 Go 1.22 或更高版本。" >&2
    exit 1
fi

for program in llama-launcher llama-updater; do
    if [[ ! -d "${ROOT_DIR}/cmd/${program}" ]]; then
        echo "错误: 缺少程序入口 cmd/${program}" >&2
        exit 1
    fi
done
TARGET_OS="${GOOS:-linux}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"

VERSION="${VERSION:-$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || printf 'dev')}"
COMMIT="${COMMIT:-$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || printf 'unknown')}"
BUILD_DATE="${BUILD_DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}"
MODULE_PATH="$(cd "${ROOT_DIR}" && go list -m)"

OUTPUT_SUFFIX=""
if [[ "${TARGET_OS}" == "windows" ]]; then
    OUTPUT_SUFFIX=".exe"
fi
OUTPUT_ROOT="${ROOT_DIR}/dist/${TARGET_OS}-${TARGET_ARCH}/llama.cpp"
mkdir -p "${OUTPUT_ROOT}/bin"
ASSET_ROOT="${ROOT_DIR}/dist/${TARGET_OS}-${TARGET_ARCH}"

LDFLAGS="-s -w"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.Version=${VERSION}"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.Commit=${COMMIT}"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.BuildDate=${BUILD_DATE}"

for program in llama-launcher llama-updater; do
    if [[ "${program}" == "llama-launcher" ]]; then
        output_file="${OUTPUT_ROOT}/bin/${program}${OUTPUT_SUFFIX}"
    else
        output_file="${ASSET_ROOT}/llama-updater-${VERSION}-${TARGET_OS}-${TARGET_ARCH}${OUTPUT_SUFFIX}"
    fi
    (
        cd "${ROOT_DIR}"
        GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" CGO_ENABLED=0 go build \
            -trimpath \
            -ldflags="${LDFLAGS}" \
            -o "${output_file}" \
            "./cmd/${program}"
    )
    echo "输出文件: ${output_file}"
done

echo "构建完成"
echo "版本: ${VERSION}"
echo "提交: ${COMMIT}"
echo "构建时间: ${BUILD_DATE}"
