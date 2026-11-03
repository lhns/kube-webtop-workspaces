# shellcheck shell=bash
# Replace a file the image wrote into the user's home only while the user has not changed it.
#   webtop-template write <target> <source>          install source at target, record its hash
#   webtop-template unmodified <target> [<known>...]  exit 0 if target is what `write` last put
#                                                    there, or byte-identical to a known file
# Hashes are kept per target path in $WEBTOP_TEMPLATE_STATE (one "<sha256>  <target>" line each).
# Give targets as paths under /persist/home/user: the home's other paths are bind mounts of it and
# would be different keys.
set -euo pipefail # as writeShellApplication runs it

state=${WEBTOP_TEMPLATE_STATE:-/persist/home/user/.local/state/webtop/templates.sha256}

usage() { echo "usage: webtop-template write <target> <source> | unmodified <target> [<known>...]" >&2; exit 2; }

[ $# -ge 2 ] || usage
cmd=$1 target=$2
shift 2
case $cmd in
  write)
    [ $# -eq 1 ] || usage
    mkdir -p "$(dirname "$target")" "$(dirname "$state")"
    cp -f "$1" "$target.webtop-tmp" # -f: a leftover from an interrupted write is read-only
    chmod 644 "$target.webtop-tmp" # store files are read-only
    mv "$target.webtop-tmp" "$target"
    sum=$(sha256sum < "$target" | cut -d' ' -f1)
    { if [ -f "$state" ]; then
        while IFS= read -r l; do [ "${l#*  }" = "$target" ] || printf '%s\n' "$l"; done < "$state"
      fi
      printf '%s  %s\n' "$sum" "$target"
    } > "$state.tmp"
    mv "$state.tmp" "$state"
    ;;
  unmodified)
    [ -f "$target" ] && [ ! -L "$target" ] || exit 1
    sum=$(sha256sum < "$target" | cut -d' ' -f1)
    if [ -f "$state" ] && grep -qxF "$sum  $target" "$state"; then exit 0; fi
    for k in "$@"; do
      if cmp -s "$target" "$k"; then exit 0; fi
    done
    exit 1
    ;;
  *) usage ;;
esac
