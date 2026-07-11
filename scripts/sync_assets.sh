#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/sync_assets.sh [options]

Refresh the asset repository from the original AI-native fuzz workspace.

Options:
  --source-root PATH      Source workspace root. Default: /Users/bba/pc
  --delete               Mirror selected directories by clearing copied files first.
  --refresh-bug-index    Refresh docs/bug-index/SEVERE_BUGS_FROM_DB.md from remote found_bug.
  --commit               Commit changes if the repository becomes dirty.
  --push                 Push after commit. Implies --commit.
  -h, --help             Show this help.

Environment:
  SOURCE_ROOT            Alternative way to set the source workspace root.
USAGE
}

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_root="${SOURCE_ROOT:-/Users/bba/pc}"
delete_mode=0
commit_mode=0
push_mode=0
refresh_bug_index=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-root)
      source_root="$2"
      shift 2
      ;;
    --source-root=*)
      source_root="${1#*=}"
      shift
      ;;
    --delete)
      delete_mode=1
      shift
      ;;
    --refresh-bug-index)
      refresh_bug_index=1
      shift
      ;;
    --commit)
      commit_mode=1
      shift
      ;;
    --push)
      commit_mode=1
      push_mode=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ ! -d "$source_root" ]]; then
  echo "source root does not exist: $source_root" >&2
  exit 1
fi

clear_dir_files() {
  local dir="$1"
  if [[ "$delete_mode" -eq 1 && -d "$dir" ]]; then
    find "$dir" -maxdepth 1 -type f -delete
  fi
}

copy_if_exists() {
  local src="$1"
  local dst_dir="$2"
  [[ -f "$src" ]] || return 0
  mkdir -p "$dst_dir"
  rsync -a "$src" "$dst_dir/"
}

sync_glob() {
  local dst_dir="$1"
  shift
  mkdir -p "$dst_dir"
  clear_dir_files "$dst_dir"
  while [[ $# -gt 0 ]]; do
    find "$source_root" -maxdepth 1 -type f -name "$1" -exec rsync -a {} "$dst_dir/" \;
    shift
  done
}

sync_core_docs() {
  local dst="$repo_root/docs/core"
  mkdir -p "$dst"
  for name in \
    ai-native-autonomous-loop.md \
    ai-native-proof-obligation-methodology-v2.md \
    ai-native-proof-obligation-catalog.md \
    ai-native-selector-ledger.md \
    ai-native-root-cause-ledger.md \
    ai-native-oracle-library.md \
    ai-native-asset-validation.md \
    ai-native-evolving-system.md \
    ai-native-ddl-methodology.md \
    ai-native-ddl-github-heldout-methodology.md \
    ai-native-ddl-reference-matrix.md \
    ai-native-concurrency-harness.md \
    ai-native-perf-oracle-library.md \
    ai-native-heldout-blind-test.md \
    ai-native-ddl-next-owner-scan.md \
    ai-native-s7-hidden-input-getter-scan.md
  do
    copy_if_exists "$source_root/$name" "$dst"
  done
}

sync_go_probes() {
  local dst="$repo_root/scaffolds/go-probes"
  mkdir -p "$dst"
  clear_dir_files "$dst"
  if [[ -d "$source_root/ai-native-probes" ]]; then
    find "$source_root/ai-native-probes" -maxdepth 1 -type f \( -name '*.go' -o -name 'go.mod' -o -name 'go.sum' \) -exec rsync -a {} "$dst/" \;
  fi
}

sync_tidb_tests() {
  local dst="$repo_root/scaffolds/tidb-tests"
  mkdir -p "$dst"
  clear_dir_files "$dst"
  for rel in \
    pkg/ddl/ai_native_reorg_grpc_probe_test.go \
    pkg/ddl/ai_native_retry_probe_test.go \
    pkg/ddl/ingest/ai_native_retry_classifier_test.go \
    pkg/ddl/ingest/ai_native_sql_cancel_test.go \
    pkg/ddl/tests/indexmerge/ai_native_mvi_merge_probe_test.go \
    pkg/ddl/tests/indexmerge/ai_native_mvi_probe_test.go \
    pkg/sessiontxn/txn_context_autocommit_probe_test.go \
    tests/realtikvtest/addindextest3/ai_native_checkpoint_probe_test.go \
    tests/realtikvtest/addindextest4/ai_native_global_async_test.go \
    tests/realtikvtest/addindextest4/ai_native_issue62531_pool_probe_test.go \
    tests/realtikvtest/addindextest4/ai_native_mvi_owner_test.go
  do
    copy_if_exists "$source_root/tidb/$rel" "$dst"
  done
}

mkdir -p "$repo_root/docs/handoff" "$repo_root/assets/bug-db" "$repo_root/assets/store"
copy_if_exists "$source_root/ai-native-fuzz-handoff.md" "$repo_root/docs/handoff"
copy_if_exists "$source_root/ai-native-found-bug-pending.sql" "$repo_root/assets/bug-db"

sync_core_docs
sync_glob "$repo_root/docs/bug-drafts" 'ai-native-*-draft.md'
sync_glob "$repo_root/docs/method-cases" 'ai-native-id*-method-case.md'
sync_glob "$repo_root/scaffolds/top-level" 'ai_native_*' 'ai-native-*.sql'
sync_go_probes
sync_tidb_tests

if [[ -d "$source_root/ai-native-assets" ]]; then
  if [[ "$delete_mode" -eq 1 ]]; then
    rsync -a --delete --exclude '__pycache__' "$source_root/ai-native-assets/" "$repo_root/assets/store/"
  else
    rsync -a --exclude '__pycache__' "$source_root/ai-native-assets/" "$repo_root/assets/store/"
  fi
fi

if [[ "$refresh_bug_index" -eq 1 ]]; then
  "$repo_root/scripts/refresh_severe_bug_index.sh"
fi

if [[ "$commit_mode" -eq 1 ]]; then
  if [[ -n "$(git -C "$repo_root" status --short)" ]]; then
    git -C "$repo_root" add .
    git -C "$repo_root" commit -m "Sync AI-native fuzz assets"
  else
    echo "no changes to commit"
  fi
fi

if [[ "$push_mode" -eq 1 ]]; then
  git -C "$repo_root" push
fi

git -C "$repo_root" status --short
