#!/usr/bin/env bash
# Build easysh: fetch + build the WebUI, embed it, and export the binaries.
#
# easysh is NOT a container service — it ships as plain binaries. The SPA lives
# in the easysh-webui repo and is NOT committed here, so this script builds it
# into pkg/webui/dist/ (the go:embed path) before compiling.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${DIR}"

WEBUI_REPO="${EASYSH_WEBUI_REPO:-https://github.com/easydev-org/easysh-webui.git}"
WEBUI_REF="${EASYSH_WEBUI_REF:-main}"
OUT="${OUT:-dist}"            # where the binaries land
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"

# 1. Build the SPA into the go:embed path.
if command -v npm >/dev/null 2>&1; then
  echo "==> building webui (${WEBUI_REPO}@${WEBUI_REF})"
  TMP="$(mktemp -d)"
  trap 'rm -rf "${TMP}"' EXIT
  git clone --depth 1 --branch "${WEBUI_REF}" "${WEBUI_REPO}" "${TMP}"
  ( cd "${TMP}" && npm ci --no-audit --no-fund && OUT_DIR="${DIR}/pkg/webui/dist" npm run build )
else
  echo "!! npm not found — using the existing pkg/webui/dist (if any)" >&2
fi

# 2. Compile (the SPA is now embedded via go:embed).
mkdir -p "${OUT}"
echo "==> building easyshd + easysh (${GOOS}/${GOARCH})"
GOOS="${GOOS}" GOARCH="${GOARCH}" CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o "${OUT}/easyshd" ./cmd/easyshd
GOOS="${GOOS}" GOARCH="${GOARCH}" CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w" -o "${OUT}/easysh" ./cmd/easysh
echo "==> done: ${OUT}/easyshd ${OUT}/easysh"
