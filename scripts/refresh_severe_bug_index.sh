#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$repo_root/docs/bug-index/SEVERE_BUGS_FROM_DB.md"
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

mysql_bin="${MYSQL_BIN:-}"
if [[ -z "$mysql_bin" ]]; then
  for candidate in \
    /opt/homebrew/Cellar/mysql-client@8.0/8.0.42/bin/mysql \
    /usr/local/mysql-8.3.0-macos14-arm64/bin/mysql \
    mysql
  do
    if command -v "$candidate" >/dev/null 2>&1 || [[ -x "$candidate" ]]; then
      mysql_bin="$candidate"
      break
    fi
  done
fi

"$mysql_bin" --login-path=tidbbug --ssl-mode=VERIFY_IDENTITY --ssl-ca=/etc/ssl/cert.pem -D test --batch --raw --skip-column-names -e \
  "SELECT id,status,severity,category,ddl_op,feature,root_cause_id,title,COALESCE(issue_url,'') FROM found_bug WHERE severity='high' ORDER BY id" > "$tmp"

mkdir -p "$(dirname "$out")"
{
  echo "# Severe Bugs From found_bug"
  echo
  echo "Generated at: $(date '+%Y-%m-%d %H:%M:%S %z')"
  echo
  echo "| ID | Status | Severity | Category | DDL / op | Feature | Root cause ID | Title | Issue |"
  echo "| --- | --- | --- | --- | --- | --- | --- | --- | --- |"
  while IFS=$'\t' read -r id status severity category ddl_op feature root_cause_id title issue_url; do
    category="${category//|/\\|}"
    ddl_op="${ddl_op//|/\\|}"
    feature="${feature//|/\\|}"
    title="${title//|/\\|}"
    if [[ -n "$issue_url" ]]; then
      issue="[$issue_url]($issue_url)"
    else
      issue=""
    fi
    echo "| $id | $status | $severity | $category | $ddl_op | $feature | \`$root_cause_id\` | $title | $issue |"
  done < "$tmp"
} > "$out"

echo "wrote $out"
