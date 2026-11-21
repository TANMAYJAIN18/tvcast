#!/bin/bash
# Builds the TV Cast Android app (APK) with the plain Android SDK tools: no Gradle,
# no third-party libraries. Output: dist/TVCast-android.apk
#
# Needs: Go, JDK 11+, Android SDK with platforms;android-34 and build-tools;34.0.0
#   ANDROID_HOME   Android SDK folder (auto-detected for Android Studio / Homebrew installs)
#   TVCAST_KEYSTORE / TVCAST_KEYSTORE_PASS   release signing key (created on first build if missing)
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$PWD"
VERSION="${VERSION:-$(cat VERSION)}"
IFS=. read -r MA MI PA <<< "$VERSION"
VCODE=$(( MA * 10000 + MI * 100 + PA ))

for d in "${ANDROID_HOME:-}" "$HOME/Library/Android/sdk" /opt/homebrew/share/android-commandlinetools \
         /usr/local/share/android-commandlinetools "$HOME/Android/Sdk"; do
  [ -n "$d" ] && [ -d "$d/build-tools" ] && { SDK="$d"; break; }
done
[ -n "${SDK:-}" ] || { echo "Android SDK not found (set ANDROID_HOME)"; exit 1; }
BT="$SDK/build-tools/$(ls "$SDK/build-tools" | sort -V | tail -1)"
JAR="$SDK/platforms/android-34/android.jar"
[ -f "$JAR" ] || JAR="$(ls -d "$SDK"/platforms/android-*/android.jar | sort -V | tail -1)"
if [ -z "${JAVA_HOME:-}" ]; then
  for j in /opt/homebrew/opt/openjdk@17 /opt/homebrew/opt/openjdk /usr/local/opt/openjdk@17; do
    [ -x "$j/bin/javac" ] && { export JAVA_HOME="$j"; break; }
  done
fi
JAVAC="${JAVA_HOME:+$JAVA_HOME/bin/}javac"; KEYTOOL="${JAVA_HOME:+$JAVA_HOME/bin/}keytool"

B="$ROOT/build/android"; rm -rf "$B"; mkdir -p "$B"/{res,gen,classes,dex,apk/lib}
echo "==> engine for Android (arm64, armv7, x86_64)"
LDF="-s -w -X main.Version=$VERSION"
(cd engine
 CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags "$LDF" -o "$B/apk/lib/arm64-v8a/libtvcast.so" .
 # 32-bit Android (<= 10) kills apps that try the newer time64 syscalls instead of returning
 # "not supported", so Go's built-in fallback never runs. Patch the runtime (for this build
 # only) to use the classic syscalls directly.
 RT="$(go env GOROOT)/src/runtime/os_linux32.go"
 sed -e 's/if !isFutexTime32bitOnly.Load() {/if false \&\& !isFutexTime32bitOnly.Load() {/' \
     -e 's/if !isSetTime32bitOnly.Load() {/if false \&\& !isSetTime32bitOnly.Load() {/' "$RT" > "$B/os_linux32.go"
 grep -q 'if false && !isFutexTime32bitOnly' "$B/os_linux32.go" || { echo "runtime patch failed (Go changed?)"; exit 1; }
 printf '{"Replace":{"%s":"%s"}}' "$RT" "$B/os_linux32.go" > "$B/overlay.json"
 CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -overlay "$B/overlay.json" -trimpath -tags tvandroid -ldflags "$LDF" -o "$B/apk/lib/armeabi-v7a/libtvcast.so" .
 CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags tvandroid -ldflags "$LDF" -o "$B/apk/lib/x86_64/libtvcast.so" .)

echo "==> resources"
sed -e "s/__VERSION_CODE__/$VCODE/" -e "s/__VERSION__/$VERSION/" android/AndroidManifest.xml > "$B/AndroidManifest.xml"
"$BT/aapt2" compile --dir android/res -o "$B/res/compiled.zip"
"$BT/aapt2" link -I "$JAR" --manifest "$B/AndroidManifest.xml" -o "$B/base.apk" --java "$B/gen" \
  --min-sdk-version 24 --target-sdk-version 34 --version-code "$VCODE" --version-name "$VERSION" ${DEBUGGABLE:+--debug-mode} "$B/res/compiled.zip"

echo "==> java"
find android/src "$B/gen" -name '*.java' | sed 's/.*/"&"/' > "$B/sources.txt"
"$JAVAC" -nowarn -source 11 -target 11 -encoding UTF-8 -classpath "$JAR" -d "$B/classes" \
  @"$B/sources.txt" 2>&1 | grep -v 'bootstrap class path' || true
[ -n "$(find "$B/classes" -name '*.class')" ] || { echo "javac failed"; exit 1; }
(cd "$B/classes" && find . -name '*.class' > ../classes.txt && "$BT/d8" --release --min-api 24 --lib "$JAR" --output "$B/dex" @../classes.txt)

echo "==> package"
# package <name> <abi...>: one APK containing only the given CPU types
package() {
  local name="$1"; shift
  local stage="$B/stage-$name"; rm -rf "$stage"; mkdir -p "$stage/lib"
  for abi in "$@"; do cp -R "$B/apk/lib/$abi" "$stage/lib/"; done
  cp "$B/base.apk" "$B/$name-unsigned.apk"
  (cd "$B/dex" && zip -q -X "$B/$name-unsigned.apk" classes.dex)
  (cd "$stage" && zip -q -X -r "$B/$name-unsigned.apk" lib)
  "$BT/zipalign" -f -p 4 "$B/$name-unsigned.apk" "$B/$name-aligned.apk"
}
package TVCast-android arm64-v8a armeabi-v7a x86_64   # works on every device
package TVCast-android-64bit arm64-v8a                # modern phones/tablets (smaller)
package TVCast-android-32bit armeabi-v7a              # older 32-bit devices

KS="${TVCAST_KEYSTORE:-$HOME/.tvcast/android-release.jks}"
PASSFILE="$(dirname "$KS")/android-release.pass"
if [ -z "${TVCAST_KEYSTORE_PASS:-}" ]; then
  if [ ! -f "$KS" ]; then
    echo "==> creating your app signing key at $KS (back this folder up: updates need the same key)"
    mkdir -p "$(dirname "$KS")"; chmod 700 "$(dirname "$KS")"
    head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$PASSFILE"; chmod 600 "$PASSFILE"
    "$KEYTOOL" -genkeypair -keystore "$KS" -storepass "$(cat "$PASSFILE")" -keypass "$(cat "$PASSFILE")" \
      -alias tvcast -keyalg RSA -keysize 3072 -validity 18250 -dname "CN=TV Cast" >/dev/null 2>&1
  fi
  TVCAST_KEYSTORE_PASS="$(cat "$PASSFILE")"
fi
mkdir -p dist
for n in TVCast-android TVCast-android-64bit TVCast-android-32bit; do
  "$BT/apksigner" sign --ks "$KS" --ks-pass "pass:$TVCAST_KEYSTORE_PASS" --ks-key-alias tvcast \
    --out "dist/$n.apk" "$B/$n-aligned.apk"
  "$BT/apksigner" verify "dist/$n.apk"
  echo "==> dist/$n.apk ($(du -h "dist/$n.apk" | cut -f1), version $VERSION)"
done
rm -f dist/*.apk.idsig
