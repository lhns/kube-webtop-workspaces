# profile-hook of the patched `nix profile` (ADR 0013): applies a changed packages.json with
# home-manager. Exits 0 only once it is applied; on failure the previous generation is active again.

# Profile changes, home-manager's own install of its generation included: nothing to apply. Must
# stay cheap, it runs for every one of them.
[ "${NIX_PROFILE_BACKEND:-}" = declaration ] || exit 0

dir=$(dirname "$NIX_PROFILE_DECLARATION")
# A flake in a git repo (its own, or a dotfiles repo around it) sees only files git knows.
if git -C "$dir" rev-parse --git-dir >/dev/null 2>&1; then
  if git -C "$dir" check-ignore -q -- "$NIX_PROFILE_DECLARATION"; then
    echo "webtop-profile-hook: git ignores $NIX_PROFILE_DECLARATION, so the flake cannot read it; un-ignore it" >&2
    exit 1
  fi
  git -C "$dir" add --intent-to-add -- "$NIX_PROFILE_DECLARATION"
fi

gen="${XDG_STATE_HOME:-$HOME/.local/state}/nix/profiles/home-manager"
prev=$(readlink -f "$gen" || true)
rc=0
home-manager switch --flake "$dir" || rc=$?
[ "$rc" = 0 ] && exit 0
cur=$(readlink -f "$gen" || true)
if [ -n "$prev" ] && [ "$cur" != "$prev" ]; then
  echo "webtop-profile-hook: re-activating the previous home-manager generation" >&2
  "$prev/activate" || echo "webtop-profile-hook: re-activating $prev failed" >&2
fi
exit "$rc"
