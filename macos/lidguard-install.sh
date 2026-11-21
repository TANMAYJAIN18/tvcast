#!/bin/sh
# TV Cast lid guard installer (run as root, e.g. via the app's "Turn on" button or sudo).
# Installs a tiny root helper: while TV Cast is streaming, the Mac stays awake even with
# the lid closed. When nothing plays, or the battery drops below 10%, normal sleep returns.
set -e
[ "$(id -u)" = 0 ] || { echo "Run with sudo"; exit 1; }
DIR="/Library/Application Support/TVCast"
mkdir -p "$DIR"
cat > "$DIR/sleepguard.sh" <<'GUARD'
#!/bin/sh
FLAG=/private/tmp/tvcast-active
state=""
cleanup() { /usr/bin/pmset -a disablesleep 0 >/dev/null 2>&1; exit 0; }
trap cleanup TERM INT HUP
while :; do
  want=0
  if [ -f "$FLAG" ] && [ ! -L "$FLAG" ]; then
    age=$(( $(date +%s) - $(stat -f %m "$FLAG" 2>/dev/null || echo 0) ))
    [ "$age" -lt 120 ] && want=1
  fi
  if [ "$want" = 1 ]; then   # never drain a battery to zero with the lid closed
    batt=$(/usr/bin/pmset -g batt | grep -o '[0-9]*%' | head -1 | tr -d %)
    if /usr/bin/pmset -g batt | grep -q discharging && [ -n "$batt" ] && [ "$batt" -lt 10 ]; then want=0; fi
  fi
  if [ "$want" != "$state" ]; then /usr/bin/pmset -a disablesleep "$want" >/dev/null 2>&1; state=$want; fi
  sleep 15 & wait $!
done
GUARD
chown root:wheel "$DIR" "$DIR/sleepguard.sh"; chmod 755 "$DIR" "$DIR/sleepguard.sh"
PL=/Library/LaunchDaemons/com.tvcast.sleepguard.plist
cat > "$PL" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.tvcast.sleepguard</string>
  <key>ProgramArguments</key><array><string>/bin/sh</string><string>$DIR/sleepguard.sh</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
PLIST
chown root:wheel "$PL"; chmod 644 "$PL"
launchctl bootout system/com.tvcast.sleepguard 2>/dev/null || true
launchctl bootstrap system "$PL"
echo "Lid-closed streaming enabled."
