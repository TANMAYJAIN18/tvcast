#!/bin/bash
# TV Cast installer for macOS and Linux.
#   curl -fsSL https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/install.sh | bash
# Safe to run again any time: it updates/repairs and keeps your TVs and settings.
set -euo pipefail

REPO="TANMAYJAIN18/tvcast"
RELEASE="https://github.com/$REPO/releases/latest/download"
RAW="https://raw.githubusercontent.com/$REPO/main"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo .)"

say()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m ✓\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m !\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m ✗ %s\033[0m\n' "$*"; exit 1; }
ask()  { # ask "question" default(y/n) -> returns 0 for yes
  local a d="$2"
  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    printf '%s [%s] ' "$1" "$([ "$d" = y ] && echo Y/n || echo y/N)" > /dev/tty
    read -r a < /dev/tty || a=""
  else a=""; fi
  a="${a:-$d}"; [[ "$a" =~ ^[Yy] ]]
}

TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT

fetch() { # fetch <asset-name> <dest>: local dist/ build if present, else latest GitHub release
  if [ -f "$HERE/dist/$1" ]; then cp "$HERE/dist/$1" "$2"
  else curl -fL --retry 3 --progress-bar "$RELEASE/$1" -o "$2" || die "Download failed: $1"; fi
}

install_mac() {
  local major; major="$(sw_vers -productVersion | cut -d. -f1)"
  [ "$major" -ge 12 ] || die "TV Cast needs macOS 12 or newer"
  say "Installing TV Cast for macOS"

  # stop old copies (app window, background engine, and the early Python prototype)
  osascript -e 'tell application id "com.tvcast.app" to quit' >/dev/null 2>&1 || true
  launchctl bootout "gui/$(id -u)/com.tvcast.engine" >/dev/null 2>&1 || true
  pkill -f "TV Cast.app/Contents/Resources/tvcast-engine" >/dev/null 2>&1 || true
  pkill -f "TV Cast.app/Contents/Resources/server.py" >/dev/null 2>&1 || true
  local old="$HOME/Applications/TV Cast.app"
  if [ -d "$old" ] && [ "$(defaults read "$old/Contents/Info" CFBundleIdentifier 2>/dev/null)" = "local.tvcast" ]; then
    rm -rf "$old"; ok "Removed the old prototype app"
  fi

  local dest="/Applications"
  [ -w "$dest" ] || { dest="$HOME/Applications"; mkdir -p "$dest"; }
  if [ -d "$HERE/dist/TV Cast.app" ]; then
    rm -rf "$dest/TV Cast.app"; ditto "$HERE/dist/TV Cast.app" "$dest/TV Cast.app"
  else
    fetch TVCast-macOS.zip "$TMP/app.zip"
    rm -rf "$dest/TV Cast.app"; ditto -x -k "$TMP/app.zip" "$dest"
  fi
  xattr -dr com.apple.quarantine "$dest/TV Cast.app" 2>/dev/null || true
  ok "Installed $dest/TV Cast.app"

  ln -sfn "$dest/TV Cast.app" "$HOME/Desktop/TV Cast" && ok "Desktop shortcut: TV Cast"

  if [ -f /Library/LaunchDaemons/com.tvcast.sleepguard.plist ]; then
    ok "Lid-closed streaming already on"
  elif [ "${TVCAST_LID:-ask}" != no ] && ask "Keep streaming when the MacBook lid is closed? (asks your Mac password once)" y; then
    if sudo /bin/sh "$dest/TV Cast.app/Contents/Resources/lidguard-install.sh"; then ok "Lid-closed streaming on"
    else warn "Skipped. You can turn it on later in TV Cast ▸ ⚙︎ settings"; fi
  fi

  say "Starting TV Cast"
  open "$dest/TV Cast.app"
  ok "Done. TV Cast also runs quietly in the background after every login."
  echo "   First time: click Allow when macOS asks about local network, folders and incoming connections."
}

install_linux() {
  local arch
  case "$(uname -m)" in
    x86_64|amd64) arch=x64 ;; aarch64|arm64) arch=arm64 ;;
    *) die "Unsupported CPU: $(uname -m)" ;;
  esac
  say "Installing TV Cast for Linux ($arch)"
  local bin="$HOME/.local/bin" apps="$HOME/.local/share/applications" icons="$HOME/.local/share/icons"
  mkdir -p "$bin" "$apps" "$icons"
  systemctl --user stop tvcast.service >/dev/null 2>&1 || true
  pkill -x tvcast >/dev/null 2>&1 || true
  fetch "tvcast-linux-$arch" "$TMP/tvcast"
  install -m 755 "$TMP/tvcast" "$bin/tvcast"
  if [ -f "$HERE/assets/icon.png" ]; then cp "$HERE/assets/icon.png" "$icons/tvcast.png"
  else curl -fsSL "$RAW/assets/icon.png" -o "$icons/tvcast.png" || true; fi
  ok "Installed $bin/tvcast"

  cat > "$apps/tvcast.desktop" <<DESK
[Desktop Entry]
Type=Application
Name=TV Cast
Comment=Stream movies to your TV in full quality
Exec=$bin/tvcast
Icon=$icons/tvcast.png
Terminal=false
Categories=AudioVideo;Video;Network;
DESK
  local desk; desk="$(xdg-user-dir DESKTOP 2>/dev/null || echo "$HOME/Desktop")"
  if [ -d "$desk" ]; then
    cp "$apps/tvcast.desktop" "$desk/tvcast.desktop"; chmod +x "$desk/tvcast.desktop"
    gio set "$desk/tvcast.desktop" metadata::trusted true >/dev/null 2>&1 || true
    ok "Desktop shortcut"
  fi

  if command -v systemctl >/dev/null && systemctl --user show-environment >/dev/null 2>&1; then
    mkdir -p "$HOME/.config/systemd/user"
    cat > "$HOME/.config/systemd/user/tvcast.service" <<UNIT
[Unit]
Description=TV Cast streaming engine
After=network-online.target

[Service]
ExecStart=$bin/tvcast --background
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
UNIT
    systemctl --user daemon-reload
    systemctl --user enable --now tvcast.service
    ok "Background engine: starts at login, restarts itself if it stops"
  else
    mkdir -p "$HOME/.config/autostart"
    sed "s|Exec=.*|Exec=$bin/tvcast --background|" "$apps/tvcast.desktop" > "$HOME/.config/autostart/tvcast.desktop"
    nohup "$bin/tvcast" --background >/dev/null 2>&1 &
    ok "Background engine: starts at login"
  fi
  sleep 1; "$bin/tvcast" >/dev/null 2>&1 &
  ok "Done. Open TV Cast from your apps menu or desktop."
}

case "$(uname -s)" in
  Darwin) install_mac ;;
  Linux) install_linux ;;
  *) die "On Windows use: irm https://raw.githubusercontent.com/$REPO/main/install.ps1 | iex" ;;
esac
