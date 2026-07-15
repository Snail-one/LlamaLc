#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="${SCRIPT_DIR}"

if ! command -v go >/dev/null 2>&1; then
    echo "错误: 找不到 Go，请先安装 Go 1.22 或更高版本。" >&2
    exit 1
fi

COMMAND_DIRS=()
while IFS= read -r -d '' command_dir; do
    COMMAND_DIRS+=("${command_dir}")
done < <(find "${ROOT_DIR}/cmd" -mindepth 1 -maxdepth 1 -type d -print0)

if [[ ${#COMMAND_DIRS[@]} -ne 1 ]]; then
    echo "错误: cmd 目录下必须有且只能有一个程序入口，当前找到 ${#COMMAND_DIRS[@]} 个。" >&2
    exit 1
fi

ENTRY_DIR="${COMMAND_DIRS[0]}"
APP_NAME="$(basename -- "${ENTRY_DIR}")"
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
OUTPUT_FILE="${OUTPUT_ROOT}/bin/${APP_NAME}${OUTPUT_SUFFIX}"

mkdir -p "${OUTPUT_ROOT}/bin"

LDFLAGS="-s -w"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.Version=${VERSION}"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.Commit=${COMMIT}"
LDFLAGS+=" -X ${MODULE_PATH}/internal/version.BuildDate=${BUILD_DATE}"

(
    cd "${ROOT_DIR}"
    GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" CGO_ENABLED=0 go build \
        -trimpath \
        -ldflags="${LDFLAGS}" \
        -o "${OUTPUT_FILE}" \
        "./cmd/${APP_NAME}"
)

echo "构建完成"
echo "输出文件: ${OUTPUT_FILE}"
echo "版本: ${VERSION}"
echo "提交: ${COMMIT}"
echo "构建时间: ${BUILD_DATE}"
