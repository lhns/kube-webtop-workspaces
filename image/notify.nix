# webtop-notify (webtop-notify.sh) for the desktop user, whoever calls it.
{ pkgs, uid }:

pkgs.writeShellApplication {
  name = "webtop-notify";
  runtimeInputs = with pkgs; [ coreutils util-linux libnotify ];
  text = "uid=${uid}\n" + builtins.readFile ./webtop-notify.sh;
}
