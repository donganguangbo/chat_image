#!/usr/bin/env bash
# Check every pin in docker/comfy/node_pins.txt: does the commit still exist
# upstream, and does its __init__.py match what production runs?
#
#   bash scripts/verify_node_pins.sh              # existence only (fast)
#   bash scripts/verify_node_pins.sh --blobs      # also compare blob SHAs
#
# --blobs needs a reference list of production blob SHAs (<dir>|<blob>), either
# at docker/comfy/node_blobs.txt or via BLOBS=<path>. Regenerate it with:
#   ssh <server> 'cd <comfy>/custom_nodes && for d in */; do \
#       [ -f "$d/__init__.py" ] && echo "${d%/}|$(git hash-object "$d/__init__.py")"; done'
#
# Run this after bumping a pin, or when a build starts failing — a vanished
# commit (force-push, repo deletion) shows up here rather than mid-build.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PINS="$PROJECT_ROOT/docker/comfy/node_pins.txt"
BLOBS="${BLOBS:-$PROJECT_ROOT/docker/comfy/node_blobs.txt}"
CHECK_BLOBS=0
[[ "${1:-}" == "--blobs" ]] && CHECK_BLOBS=1

[[ -f "$PINS" ]] || { echo "missing $PINS" >&2; exit 1; }
if [[ "$CHECK_BLOBS" -eq 1 && ! -f "$BLOBS" ]]; then
  echo "--blobs needs $BLOBS (see the header of this script)" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

ok=0; gone=0; mismatch=0
while IFS='|' read -r dir repo commit; do
  [[ -z "${dir:-}" || "$dir" == \#* ]] && continue
  # Does the commit still exist? ls-remote is cheap and needs no clone.
  if ! git ls-remote --exit-code "https://github.com/$repo.git" >/dev/null 2>&1; then
    printf "  [GONE]  %-45s repo unreachable: %s\n" "$dir" "$repo"
    gone=$((gone + 1)); continue
  fi

  if [[ "$CHECK_BLOBS" -eq 0 ]]; then
    printf "  [OK]    %-45s %s\n" "$dir" "${commit:0:8}"
    ok=$((ok + 1)); continue
  fi

  want="$(awk -F'|' -v d="$dir" '$1==d {print $2}' "$BLOBS" | head -1)"
  if [[ -z "$want" ]]; then
    printf "  [SKIP]  %-45s no reference blob\n" "$dir"
    continue
  fi
  # Blobless bare clone: trees are complete, so rev-parse <commit>:<path> works
  # without downloading file contents.
  repo_dir="$work/${dir}.git"
  if ! git clone --bare --filter=blob:none --no-checkout -q \
        "https://github.com/$repo.git" "$repo_dir" 2>/dev/null; then
    printf "  [GONE]  %-45s clone failed\n" "$dir"
    gone=$((gone + 1)); continue
  fi
  got="$(git -C "$repo_dir" rev-parse "$commit:__init__.py" 2>/dev/null || true)"
  rm -rf "$repo_dir"
  if [[ "$got" == "$want" ]]; then
    printf "  [OK]    %-45s %s\n" "$dir" "${commit:0:8}"
    ok=$((ok + 1))
  else
    printf "  [DIFF]  %-45s pinned blob %s != production %s\n" "$dir" "${got:0:8}" "${want:0:8}"
    mismatch=$((mismatch + 1))
  fi
done < "$PINS"

echo ""
echo "ok=$ok gone=$gone mismatch=$mismatch"
[[ "$gone" -gt 0 || "$mismatch" -gt 0 ]] && exit 1
exit 0
