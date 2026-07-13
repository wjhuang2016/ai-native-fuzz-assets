#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
DEPS_DIR="${TXNLAB_DEPS_DIR:-$ROOT/.txnlab/deps}"
ARTIFACTS_COMMIT="${TXNLAB_ARTIFACTS_COMMIT:-5958dc44f5f39ad4cb90b711d98571aff1bf06b6}"

if ! command -v brew >/dev/null 2>&1; then
  echo "Homebrew is required to install yq and gomplate on macOS." >&2
  exit 1
fi

missing=()
for command in yq gomplate skopeo; do
  if ! command -v "$command" >/dev/null 2>&1; then
    missing+=("$command")
  fi
done
if ((${#missing[@]})); then
  brew install "${missing[@]}"
fi

mkdir -p "$DEPS_DIR"
if [[ ! -d "$DEPS_DIR/artifacts/.git" ]]; then
  git clone --depth=1 --filter=blob:none \
    https://github.com/PingCAP-QE/artifacts.git "$DEPS_DIR/artifacts"
fi
if ! git -C "$DEPS_DIR/artifacts" cat-file -e "$ARTIFACTS_COMMIT^{commit}" 2>/dev/null; then
  git -C "$DEPS_DIR/artifacts" fetch --depth=1 origin "$ARTIFACTS_COMMIT"
fi
git -C "$DEPS_DIR/artifacts" checkout --detach "$ARTIFACTS_COMMIT" >/dev/null

printf 'yq=%s\n' "$(command -v yq)"
printf 'gomplate=%s\n' "$(command -v gomplate)"
printf 'artifacts_commit=%s\n' "$(git -C "$DEPS_DIR/artifacts" rev-parse HEAD)"
