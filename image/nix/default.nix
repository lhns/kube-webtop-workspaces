# Nix with `nix profile` backends (profile-backends.patch, ADR 0013). Only `nix profile` changes;
# its settings are client-side, the daemon (same binary) never reads them.
# path-input-fingerprint.patch: a locked `path:` input (the image's registry entries, so the users'
# nixpkgs) has no fetcher cache entry, so every evaluation of the home flake dumps all of nixpkgs to
# the store again. Still needed as of Nix 2.35.4; drop it when upstream gives path inputs a fingerprint.
{ nixVersions }:

(nixVersions.nixComponents_2_34.overrideScope (final: prev: {
  nix-cli = prev.nix-cli.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./profile-backends.patch ];
  });
  nix-fetchers = prev.nix-fetchers.overrideAttrs (old: {
    patches = (old.patches or [ ]) ++ [ ./path-input-fingerprint.patch ];
  });
  # The unit and functional test gate would rebuild and rerun everything against the patched CLI.
  nix-everything = prev.nix-everything.overrideAttrs { doCheck = false; };
})).nix-everything
