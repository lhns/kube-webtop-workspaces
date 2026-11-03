# Prints `user` when the flake in $1 has no homeConfigurations for this account but still has the one
# for the account's old name `user` (ADR 0011): that one is switched as it expects, through the
# /home/user mount, until the user updates the flake. Prints nothing otherwise, also when the flake
# does not evaluate (the switch then says why).
login=${USER:-$(id -un)}
[ -d /home/user ] || exit 0
names=$(nix eval --json "$1#homeConfigurations" --apply builtins.attrNames 2>/dev/null) || exit 0
jq -r --arg u "$login" 'if (any(.[]; . == $u) | not) and any(.[]; . == "user") then "user" else empty end' <<<"$names"
