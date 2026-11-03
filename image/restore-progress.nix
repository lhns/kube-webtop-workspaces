# webtop-restore-progress (webtop-restore-progress.sh), written by the restore scripts.
{ pkgs }:

pkgs.writeShellApplication {
  name = "webtop-restore-progress";
  runtimeInputs = with pkgs; [ coreutils jq ];
  text = builtins.readFile ./webtop-restore-progress.sh;
}
