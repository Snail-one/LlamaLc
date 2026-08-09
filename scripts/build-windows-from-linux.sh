#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
VERSION="${1:-${VERSION:-}}"
ARCH="${2:-${GOARCH:-amd64}}"
GOOS=windows GOARCH="${ARCH}" VERSION="${VERSION}" "${SCRIPT_DIR}/build.sh"
