#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
TARGET_ARCH="${2:-${GOARCH:-amd64}}"

case "${TARGET_ARCH}" in
    amd64|arm64)
        ;;
    *)
        echo "错误: 架构只支持 amd64 或 arm64，当前为 ${TARGET_ARCH}" >&2
        echo "用法: ./scripts/build-linux.sh [版本号] [amd64|arm64]" >&2
        exit 2
        ;;
esac

if [[ $# -ge 1 && -n "${1}" ]]; then
    export VERSION="${1}"
fi

GOOS=windows GOARCH="${TARGET_ARCH}" "${ROOT_DIR}/build-only.sh"

OUTPUT_ROOT="${ROOT_DIR}/dist/windows-${TARGET_ARCH}"
OUTPUT_DIR="${OUTPUT_ROOT}/llama.cpp/bin"
UPDATER="${OUTPUT_ROOT}/llama-updater-${VERSION:-$(git -C "${ROOT_DIR}" describe --tags --always --dirty 2>/dev/null || printf 'dev')}-windows-${TARGET_ARCH}.exe"

echo "Windows 部署树已生成: ${OUTPUT_DIR}"
if command -v file >/dev/null 2>&1; then
    file "${OUTPUT_DIR}/llama-launcher.exe" "${UPDATER}"
fi
