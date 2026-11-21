#!/bin/bash
# Removes TV Cast from macOS or Linux.   ./uninstall.sh          (keeps your TVs & settings)
#                                        ./uninstall.sh --all    (also deletes settings)
set -uo pipefail
ALL=0; [ "${1:-}" = "--all" ] && ALL=1
ok() { printf '\033[1;32m ✓\033[0m %s\n' "$*"; }

case "$(uname -s)" in
Darwin)
  osascript -e 'tell application id "com.tvcast.app" to quit' >/dev/null 2>&1
  launchctl bootout "gui/$(id -u)/com.tvcast.engine" >/dev/null 2>&1
  rm -f "$HOME/Library/LaunchAgents/com.tvcast.engine.plist"
  rm -rf "/Applications/TV Cast.app" "$HOME/Applications/TV Cast.app"
  rm -f "$HOME/Desktop/TV Cast"
  ok "App, background engine and Desktop shortcut removed"
  if [ -f /Library/LaunchDaemons/com.tvcast.sleepguard.plist ]; then
    echo "Removing lid-closed streaming helper (asks your password):"
    sudo /bin/sh -c 'launchctl bootout system/com.tvcast.sleepguard 2>/dev/null; rm -f /Library/LaunchDaemons/com.tvcast.sleepguard.plist; rm -rf "/Library/Application Support/TVCast"; pmset -a disablesleep 0' && ok "Lid helper removed"
  fi
  [ $ALL = 1 ] && rm -rf "$HOME/Library/Application Support/TVCast" "$HOME/Library/Logs/TVCast-engine.log" && ok "Settings deleted"
  ;;
Linux)
  systemctl --user disable --now tvcast.service >/dev/null 2>&1
  rm -f "$HOME/.config/systemd/user/tvcast.service" "$HOME/.config/autostart/tvcast.desktop"
  pkill -x tvcast >/dev/null 2>&1
  rm -f "$HOME/.local/bin/tvcast" "$HOME/.local/share/applications/tvcast.desktop" "$HOME/.local/share/icons/tvcast.png"
  rm -f "$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")/tvcast.desktop"
  ok "TV Cast removed"
  [ $ALL = 1 ] && rm -rf "${XDG_CONFIG_HOME:-$HOME/.config}/TVCast" && ok "Settings deleted"
  ;;
esac
[ $ALL = 0 ] && echo "Your TVs and settings were kept, so reinstalling picks up where you left off."
exit 0
