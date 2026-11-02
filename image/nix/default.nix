# Nix with `nix profile` backends (profile-backends.patch, ADR 0011). Only `nix profile` changes;
# its settings are client-side, the daemon (same binary) never reads them.
{ nixVersions }:

(nixVersions.nixComponents_2_34.overrideScope (final: prev: {
  nix-cli = prev.nix-cli.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./profile-backends.patch ];
  });
  # The unit and functional test gate would rebuild and rerun everything against the patched CLI.
  nix-everything = prev.nix-everything.overrideAttrs { doCheck = false; };
})).nix-everything
