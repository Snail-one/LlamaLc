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

ENTRY_DIR="$(find "${ROOT_DIR}/cmd" -mindepth 1 -maxdepth 1 -type d -print -quit)"
APP_NAME="$(basename -- "${ENTRY_DIR}")"
SOURCE_FILE="${ROOT_DIR}/dist/${APP_NAME}_windows_${TARGET_ARCH}.exe"
OUTPUT_FILE="${ROOT_DIR}/bin/${APP_NAME}.exe"
mkdir -p "${ROOT_DIR}/bin"
cp "${SOURCE_FILE}" "${OUTPUT_FILE}"

echo "Windows 启动器已复制到: ${OUTPUT_FILE}"
if command -v file >/dev/null 2>&1; then
    file "${OUTPUT_FILE}"
fi
