#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
TARGET_OS="${GOOS:-$(go env GOOS)}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"
VERSION="${VERSION:-}"
if [[ -z "${VERSION}" ]]; then VERSION="$(git -C "${PROJECT_DIR}" describe --tags --exact-match 2>/dev/null || true)"; fi
VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-$(git -C "${PROJECT_DIR}" rev-parse --short HEAD 2>/dev/null || true)}"
BUILD_DATE="${BUILD_DATE:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}"
COMMIT="${COMMIT:-unknown}"

case "${TARGET_OS}/${TARGET_ARCH}" in
  linux/amd64|linux/arm64|windows/amd64|windows/arm64) ;;
  *) echo "错误: 只支持 linux/windows 的 amd64/arm64" >&2; exit 2 ;;
esac

MODULE="$(cd "${PROJECT_DIR}" && go list -m)"
OUTPUT="${PROJECT_DIR}/dist/${TARGET_OS}-${TARGET_ARCH}/LlamaLc/bin"
mkdir -p "${OUTPUT}"
SUFFIX=""
if [[ "${TARGET_OS}" == windows ]]; then SUFFIX=".exe"; fi
LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT} -X ${MODULE}/internal/version.BuildDate=${BUILD_DATE}"

cd "${PROJECT_DIR}"
for PROGRAM in llamalc llamaup; do
  GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o "${OUTPUT}/${PROGRAM}${SUFFIX}" "./cmd/${PROGRAM}"
done
if [[ "${TARGET_OS}" == windows ]]; then
  if command -v go-winres >/dev/null 2>&1; then WINRES=(go-winres); else WINRES=(go run github.com/tc-hib/go-winres@v0.3.3); fi
  for PROGRAM in llamalc llamaup; do "${WINRES[@]}" patch --in build/windows/manifest.json --no-backup "${OUTPUT}/${PROGRAM}.exe"; done
fi
echo "构建完成: ${OUTPUT}"
