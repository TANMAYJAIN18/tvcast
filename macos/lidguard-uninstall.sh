#!/bin/sh
# Removes the TV Cast lid guard and restores normal lid-close sleep.
[ "$(id -u)" = 0 ] || { echo "Run with sudo"; exit 1; }
launchctl bootout system/com.tvcast.sleepguard 2>/dev/null || true
rm -f /Library/LaunchDaemons/com.tvcast.sleepguard.plist
rm -rf "/Library/Application Support/TVCast"
/usr/bin/pmset -a disablesleep 0
echo "Lid guard removed."
