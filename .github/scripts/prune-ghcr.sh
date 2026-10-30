#!/usr/bin/env bash
# Prints (DRY_RUN=true, the default) or deletes old dev versions of this repo's ghcr packages.
# Keeps the newest KEEP versions tagged 0.0.0-dev.*, and every version that also carries a release tag
# (vX.Y.Z, X.Y.Z, latest). A deleted version's cosign signature/attestation tags (sha256-<digest>.*) go
# with it. Untagged versions are left alone: they may be platform manifests of a kept image index.
# Also removes the retired package PHASE0_PACKAGE. Needs GH_TOKEN with packages:write.
set -euo pipefail

owner=${OWNER:-lhns}
keep=${KEEP:-10}
dry=${DRY_RUN:-true}
phase0=${PHASE0_PACKAGE:-kube-webtop-workspaces-phase0}
packages=(kube-webtop-workspaces kube-webtop-workspaces-desktop charts/kube-webtop-workspaces)

# jq filter over a package's versions: the ones to delete, as "id<TAB>tags".
filter='
  def tags: .metadata.container.tags // [];
  def dev: tags | any(test("^0[.]0[.]0-dev[.]"));
  def released: tags | any(test("^v?[0-9]+[.][0-9]+[.][0-9]+$") or . == "latest");
  ([.[] | select(dev and (released | not))] | sort_by(.created_at) | reverse | .[$keep:]) as $old
  | ($old | map(.name)) as $names
  | ($old + [.[] | select(tags | any(test("^sha256-[0-9a-f]{64}[.]") and (.[7:71] as $d | $names | index("sha256:" + $d))))])
  | unique_by(.id) | .[] | [.id, (tags | join(","))] | @tsv'

enc() { printf '%s' "${1//\//%2F}"; }
versions() { gh api --paginate "/users/$owner/packages/container/$(enc "$1")/versions?per_page=100" | jq -s 'add // []'; }
del() {
  if [ "$dry" = true ]; then echo "  would delete $2"; else gh api -X DELETE "$1" >/dev/null && echo "  deleted $2"; fi
}

echo "mode: $([ "$dry" = true ] && echo DRY RUN || echo DELETE)"

for pkg in "${packages[@]}"; do
  echo "== $pkg"
  all=$(versions "$pkg") || { echo "  cannot list (does the package exist?)"; continue; }
  n=0
  while IFS=$'\t' read -r id tags; do
    [ -n "$id" ] || continue
    n=$((n + 1))
    del "/users/$owner/packages/container/$(enc "$pkg")/versions/$id" "$id ($tags)"
  done < <(jq -r --argjson keep "$keep" "$filter" <<<"$all")
  echo "  $n of $(jq length <<<"$all") versions to delete"
done

echo "== $phase0 (retired package)"
if gh api "/users/$owner/packages/container/$(enc "$phase0")" --jq .name >/dev/null 2>&1; then
  if [ "$dry" = true ]; then
    echo "  would delete the package with $(versions "$phase0" | jq length) versions"
  # Deleting the package removes all its versions; GitHub refuses it for a package with many downloads.
  elif gh api -X DELETE "/users/$owner/packages/container/$(enc "$phase0")" >/dev/null; then
    echo "  deleted the package"
  else
    versions "$phase0" | jq -r '.[].id' | while read -r id; do
      del "/users/$owner/packages/container/$(enc "$phase0")/versions/$id" "$id"
    done
  fi
else
  echo "  not found"
fi
