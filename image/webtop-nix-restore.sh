# After an image update, copy the users' live store paths out of the store overlays webtop-init set
# aside ($N/trash-*, newest first) into the new store (ADR 0008). Runs as root, against the daemon.
# Paths the new image's store has are skipped; those only the old image's store had are substituted;
# the rest is copied from the old upper. Roots it cannot restore are left to webtop-nix-reinstall.
# Exit: 0 all restored, 1 some roots not restored, 2 skipped (no old overlay, or not enough space).
set -uo pipefail

N=${WEBTOP_NIX_PERSIST:-/persist/nix}
HOMES=${WEBTOP_HOMES:-/home}
# Free space left to the session after the copy.
RESERVE_KIB=${WEBTOP_NIX_RESTORE_RESERVE_KIB:-1048576}

log() { echo "webtop-nix-restore: $*"; }

olds=()
for d in "$N"/trash-*/; do
  [ -d "$d/upper" ] && [ -d "$d/var/db" ] && olds+=("${d%/}")
done
[ "${#olds[@]}" -gt 0 ] || { log "no old store overlay to restore from"; exit 2; }
mapfile -t olds < <(printf '%s\n' "${olds[@]}" | sort -r)

# Opened writable on purpose: the trash copy is disposable, and an immutable (read-only) open would
# ignore a WAL left by an unclean stop and miss the newest paths.
src() { echo "local?real=$1/upper&state=$1/var"; }

# GC roots of the old stores and the users' profiles (nix profile, home-manager), as store paths.
# readlink -m follows the chains (gcroots/auto -> profile generation link -> store path) without
# requiring the store path to exist yet.
roots=()
add_root() {
  local t
  t=$(readlink -m "$1") || return 0
  case $t in
    /nix/store/?*) t=${t#/nix/store/}; roots+=("/nix/store/${t%%/*}") ;;
  esac
}
while IFS= read -r -d '' l; do add_root "$l"; done < <(
  for d in "${olds[@]}"; do find "$d/var/gcroots" "$d/var/profiles" -type l -print0 2>/dev/null; done
  find "$HOMES"/*/.local/state/nix/profiles "$HOMES"/*/.local/state/home-manager/gcroots \
    -maxdepth 1 -type l -print0 2>/dev/null
)
mapfile -t roots < <(printf '%s\n' "${roots[@]}" | sort -u | grep .)

# Which old store has each root (the newest wins); roots the new store already has are done.
declare -A from
todo=()
for r in "${roots[@]}"; do
  nix path-info "$r" >/dev/null 2>&1 && continue
  for d in "${olds[@]}"; do
    if nix path-info --store "$(src "$d")" "$r" >/dev/null 2>&1; then
      from[$r]=$d; todo+=("$r"); break
    fi
  done
done
[ "${#todo[@]}" -gt 0 ] || { log "nothing to restore"; exit 0; }
log "restoring ${#todo[@]} roots: ${todo[*]}"

# The closure part the new store lacks: in an old upper (copied, counted against free space), or
# only in the old image's store (substituted).
need_kib=0
subst=()
for d in "${olds[@]}"; do
  rs=()
  for r in "${todo[@]}"; do [ "${from[$r]}" = "$d" ] && rs+=("$r"); done
  [ "${#rs[@]}" -gt 0 ] || continue
  declare -A size=()
  while read -r p s; do size[$p]=$s; done < <(nix path-info --store "$(src "$d")" -r -s "${rs[@]}")
  while read -r p; do
    [ -n "$p" ] || continue
    if [ -e "$d/upper/${p#/nix/store/}" ]; then
      need_kib=$((need_kib + size[$p] / 1024 + 1))
    else
      subst+=("$p")
    fi
  done < <(printf '%s\n' "${!size[@]}" | xargs -r nix-store --check-validity --print-invalid)
  unset size
done

avail_kib=$(df -Pk "$N" | awk 'NR == 2 { print $4 }')
if [ "$((need_kib + RESERVE_KIB))" -gt "$avail_kib" ]; then
  log "not enough space: need ${need_kib} KiB + ${RESERVE_KIB} KiB reserve, ${avail_kib} KiB free"
  exit 2
fi

if [ "${#subst[@]}" -gt 0 ]; then
  mapfile -t subst < <(printf '%s\n' "${subst[@]}" | sort -u)
  log "substituting ${#subst[@]} paths the new image's store lacks"
  # --keep-going: a path without a substitute only fails the roots that need it.
  nix-store --realise --keep-going "${subst[@]}" >/dev/null || true
fi

failed=()
for r in "${todo[@]}"; do
  ok=0
  for d in "${olds[@]}"; do
    nix path-info --store "$(src "$d")" "$r" >/dev/null 2>&1 || continue
    if nix copy --from "$(src "$d")" --no-check-sigs "$r"; then ok=1; break; fi
  done
  [ "$ok" = 1 ] || failed+=("$r")
done

if [ "${#failed[@]}" -gt 0 ]; then
  log "not restored: ${failed[*]}"
  exit 1
fi
log "all roots restored"
