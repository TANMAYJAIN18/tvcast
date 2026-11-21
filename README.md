<p align="center"><img src="assets/icon.png" width="96" alt=""></p>
<h1 align="center">TV Cast</h1>
<p align="center"><b>Play any movie from your computer, phone or tablet on your TV, at full original quality, with no lag.</b><br>
4K · HDR · Dolby Vision · Atmos · 10 GB+ files · macOS, Windows, Linux, Android → Fire TV &amp; Android TV</p>

---

Most casting apps **re-encode** video on the fly, which costs quality and causes stutter. TV Cast doesn't.
It sends the **original file** over your Wi-Fi, and the TV's own Kodi player decodes it in hardware.
Your 60 GB 4K remux plays bit-for-bit, with instant seeking.

## Install (one line)

**macOS** (Terminal) and **Linux**:
```bash
curl -fsSL https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/install.sh | bash
```

**Windows** (PowerShell):
```powershell
irm https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/install.ps1 | iex
```

**Android phones and tablets**: download the APK from [Releases](https://github.com/TANMAYJAIN18/tvcast/releases/latest) and open it:
| File | For |
|---|---|
| `TVCast-android-64bit.apk` | almost every phone and tablet from the last ~7 years (Vivo, Samsung Galaxy, Pixel, OnePlus, Xiaomi…) |
| `TVCast-android-32bit.apk` | older 32-bit devices |
| `TVCast-android.apk` | not sure? This one works on everything |

Android asks once to allow installing from your browser/files app. To update, install a newer APK over it; your TVs and settings stay.

Run the same command again any time to **update or repair**. Your TVs and settings are kept.

Prefer a manual download? Get the files from [Releases](https://github.com/TANMAYJAIN18/tvcast/releases/latest).
The macOS app isn't notarized by Apple, so after a browser download, right-click **TV Cast → Open** the first time.
The one-line installer avoids that step.

The installer gives you:
- the app (macOS: native app in Applications; Windows: Start menu; Linux: apps menu)
- a **Desktop shortcut**
- a small background engine that starts at login, so the app is always ready and **streaming continues if you close the window**
- on a MacBook, an optional **lid-closed streaming** helper (you're asked once)

## First-time setup (2 minutes, once per TV)

1. **Turn on ADB debugging on the TV**
   - **Fire TV / Fire Stick:** Settings → My Fire TV → About → click the device name 7 times → back → Developer Options → **ADB Debugging: ON**
   - **Android TV / Google TV:** Settings → Device Preferences → About → click *Build* 7 times → back → Developer options → **USB debugging: ON** (and *Network debugging* if shown)
2. Open **TV Cast** and press **Search network**. On the TV, accept **"Allow USB debugging?"** and tick **Always allow**.
3. If the TV has no Kodi, press **Install Kodi**. TV Cast downloads the official build from kodi.tv, checks its SHA-256 checksum, installs it and tunes it.
4. Click any movie. It plays.

## Android notes

- **Videos found automatically** in Movies, Download, DCIM and **SD cards / USB drives**.
- On first start, allow **Videos** and **Notifications**, then allow TV Cast to **run in the background**, so streaming continues with the screen off.
  - **Vivo / iQOO:** App info → Battery → *Allow high background power consumption*. Also lock TV Cast in Recents.
  - **Samsung:** App info → Battery → *Unrestricted*.
- The TV asks **"Allow USB debugging?"** once for each phone or tablet. Tick *Always allow*.
- A phone, a tablet and a computer can all control the same TV. They share Kodi's settings automatically.
- While streaming, TV Cast holds a CPU wake lock and a high-performance Wi-Fi lock. It releases them when nothing plays.

## Features

| | |
|---|---|
| **Full quality** | No transcoding: 4K, HDR10/HDR10+, Dolby Vision, DTS-HD/TrueHD/Atmos passthrough, all subtitles |
| **No lag** | Kodi tuned for big files: large read-ahead buffer, refresh-rate matching for smooth 24p |
| **One-click play** | Click a movie, it starts on the TV. Seek bar, pause, ±10/30 s, volume, audio language and subtitles from the computer |
| **Finds everything** | Scans Movies/Videos, Downloads, Desktop, Documents and external drives; add any folder |
| **Finds your TVs** | Network search, add by IP, reconnect, and automatic re-discovery when the router gives a TV a new address |
| **Kodi management** | Install, update (always the latest official release) and optimize Kodi from the app |
| **Stays awake while streaming** | The computer won't sleep while a TV is playing *or paused*; normal sleep when idle |
| **Lid closed** | macOS: optional helper keeps a MacBook streaming with the lid shut, only while streaming, and never below 10 % battery. Linux: automatic (systemd inhibitor). Windows: one click in settings |
| **Self-healing** | Engine restarts itself if it ever stops (launchd / systemd / Android service / built-in watchdog on Windows); drops stuck streams after 30 min without progress (Kodi reconnects on resume); caps simultaneous streams; recovers from errors instead of crashing |
| **Private and secure** | Everything stays on your network. The control panel only answers this computer; movie links use a random secret; Kodi remote control is password-protected (handled for you) |

## How it works

```
 your computer                                  your TV
┌──────────────────────────┐   Wi-Fi / LAN   ┌──────────────────────┐
│ TV Cast engine (Go)      │ ── original ──▶ │ Kodi (hardware       │
│  • HTTP range streaming  │    file bytes   │  HEVC/AV1/DV decode) │
│  • adb: launch & tune    │ ── adb ───────▶ │                      │
│  • Kodi JSON-RPC control │ ◀─ status ───── │                      │
│ UI: native window (macOS)│                 └──────────────────────┘
│     or browser (Win/Lin) │
└──────────────────────────┘
```

- **Engine** (`engine/`, Go, no dependencies): one codebase for every OS. Streams with `sendfile` and full HTTP Range support; ~300 MB/s on a laptop, far beyond what any 4K movie needs.
- **macOS app** (`macos/`, Swift): native window, menus and Dock icon. The engine runs as a per-user LaunchAgent with automatic restart.
- **adb**: a built-in ADB client written in Go (RSA auth, shell, sync push/pull, port forwarding), used on every platform. It needs no downloads and can't be broken by other tools' adb servers. On computers it reuses your existing `~/.android/adbkey`, so TVs that already trust the computer don't ask again. (`TVCAST_NATIVE_ADB=0` switches back to Google's adb tool.)
- **Android app** (`android/`, Java, no third-party libraries): runs the same engine as a foreground service and shows the same UI in a WebView.
- **ffprobe** (optional): if installed (`brew install ffmpeg`), shows resolution/HDR/audio badges.

## Tips

- Put the computer on **5 GHz Wi-Fi or Ethernet** for big 4K remuxes (they peak around 80–120 Mbit/s).
- On MacBooks, TV Cast shows a banner until **lid-closed streaming** is on. Click **Turn on** once (asks your password). Without it, macOS always sleeps when the lid closes. Keep the Mac **plugged in** for long movies with the lid closed.
- Fire TV sleeping? Press a remote button. A sleeping Fire TV may drop off Wi-Fi.
- Something odd? macOS: TV Cast menu → **Restart Engine** or **Show Log**. Logs: `~/Library/Application Support/TVCast/tvcast.log` (Windows: `%AppData%\TVCast`, Linux: `~/.config/TVCast`).

## Uninstall

```bash
# macOS / Linux (keeps settings; add --all to delete them too)
curl -fsSL https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/uninstall.sh | bash
```
```powershell
# Windows
irm https://raw.githubusercontent.com/TANMAYJAIN18/tvcast/main/uninstall.ps1 | iex
```

## Build from source

Requires Go 1.22+ (and on macOS, the Xcode Command Line Tools for the app window).
```bash
./build.sh          # everything → dist/
./build.sh mac      # macOS app only
./install.sh        # installs from your local dist/ build
./android/build.sh  # Android APKs (needs JDK 11+ and Android SDK build-tools/platform 34)
```
Releases are built by GitHub Actions when a `v*` tag is pushed.

## License

MIT
