// TV Cast for macOS — native window around the TV Cast engine.
// The engine (Go) runs as a per-user LaunchAgent so streaming keeps going when this
// window is closed, and launchd restarts it automatically if it ever stops unexpectedly.
import Cocoa
import WebKit

let enginePort = 8765
let uiURL = URL(string: "http://127.0.0.1:\(enginePort)/")!
let agentLabel = "com.tvcast.engine"

enum Engine {
    static var binary: String { Bundle.main.resourceURL!.appendingPathComponent("tvcast-engine").path }
    static var plistPath: String { NSHomeDirectory() + "/Library/LaunchAgents/\(agentLabel).plist" }
    static var domain: String { "gui/\(getuid())" }

    static func ping(timeout: TimeInterval = 1.0) -> Bool {
        var req = URLRequest(url: uiURL.appendingPathComponent("api/ping"))
        req.timeoutInterval = timeout
        let sem = DispatchSemaphore(value: 0)
        var ok = false
        URLSession.shared.dataTask(with: req) { data, _, _ in
            if let d = data, let s = String(data: d, encoding: .utf8), s.contains("tvcast") { ok = true }
            sem.signal()
        }.resume()
        _ = sem.wait(timeout: .now() + timeout + 0.5)
        return ok
    }

    @discardableResult
    static func launchctl(_ args: [String]) -> Int32 {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = args
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return -1 }
        p.waitUntilExit()
        return p.terminationStatus
    }

    static func plistContents() -> String {
        let log = NSHomeDirectory() + "/Library/Logs/TVCast-engine.log"
        func x(_ s: String) -> String {
            s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "<", with: "&lt;").replacingOccurrences(of: ">", with: "&gt;")
        }
        return """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0"><dict>
          <key>Label</key><string>\(agentLabel)</string>
          <key>ProgramArguments</key><array><string>\(x(binary))</string><string>--background</string></array>
          <key>RunAtLoad</key><true/>
          <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
          <key>ProcessType</key><string>Interactive</string>
          <key>ThrottleInterval</key><integer>5</integer>
          <key>StandardOutPath</key><string>\(x(log))</string>
          <key>StandardErrorPath</key><string>\(x(log))</string>
        </dict></plist>
        """
    }

    /// Makes sure the background engine is installed as a login item and running.
    static func ensureRunning() {
        if ping() { return }
        let want = plistContents()
        let have = try? String(contentsOfFile: plistPath, encoding: .utf8)
        if have != want {
            try? FileManager.default.createDirectory(atPath: (plistPath as NSString).deletingLastPathComponent,
                                                     withIntermediateDirectories: true)
            launchctl(["bootout", "\(domain)/\(agentLabel)"])
            try? want.write(toFile: plistPath, atomically: true, encoding: .utf8)
        }
        launchctl(["bootstrap", domain, plistPath])          // fails harmlessly if already loaded
        launchctl(["enable", "\(domain)/\(agentLabel)"])
        launchctl(["kickstart", "\(domain)/\(agentLabel)"])
        for _ in 0..<30 { if ping(timeout: 0.5) { return }; usleep(200_000) }
        // last resort: run the engine directly
        let p = Process()
        p.executableURL = URL(fileURLWithPath: binary)
        p.arguments = ["--background"]
        p.standardOutput = FileHandle.nullDevice
        p.standardError = FileHandle.nullDevice
        try? p.run()
        for _ in 0..<25 { if ping(timeout: 0.5) { return }; usleep(200_000) }
    }

    static func restart() {
        launchctl(["kickstart", "-k", "\(domain)/\(agentLabel)"])
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate {
    var window: NSWindow!
    var web: WKWebView!

    func applicationDidFinishLaunching(_ notification: Notification) {
        buildMenu()
        let cfg = WKWebViewConfiguration()
        cfg.websiteDataStore = .nonPersistent()
        web = WKWebView(frame: .zero, configuration: cfg)
        web.navigationDelegate = self
        web.uiDelegate = self
        web.setValue(false, forKey: "drawsBackground")

        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1180, height: 820),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                          backing: .buffered, defer: false)
        window.title = "TV Cast"
        window.titlebarAppearsTransparent = true
        window.backgroundColor = NSColor(red: 0.043, green: 0.047, blue: 0.063, alpha: 1)
        window.minSize = NSSize(width: 640, height: 480)
        window.isReleasedWhenClosed = false
        window.contentView = web
        window.center()
        window.setFrameAutosaveName("TVCastMainWindow")
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        showMessage("Starting TV Cast…", spinner: true)
        loadWhenReady()
    }

    func loadWhenReady() {
        DispatchQueue.global(qos: .userInitiated).async {
            Engine.ensureRunning()
            let ok = Engine.ping()
            DispatchQueue.main.async {
                if ok { self.web.load(URLRequest(url: uiURL)) }
                else { self.showMessage("TV Cast's engine didn't start. Choose TV Cast ▸ Restart Engine, or reinstall.", spinner: false) }
            }
        }
    }

    func showMessage(_ text: String, spinner: Bool) {
        let spin = spinner ? "<div class=s></div>" : ""
        web.loadHTMLString("""
        <html><body style="margin:0;height:100vh;display:grid;place-items:center;background:#0b0c10;color:#9aa1b5;
        font:15px -apple-system,sans-serif"><div style="text-align:center">\(spin)<p>\(text)</p></div>
        <style>.s{width:28px;height:28px;margin:auto;border:3px solid #333;border-top-color:#5b8cff;border-radius:50%;
        animation:r .8s linear infinite}@keyframes r{to{transform:rotate(360deg)}}</style></body></html>
        """, baseURL: nil)
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        window.makeKeyAndOrderFront(nil)
        return true
    }

    // Engine restarted or web process died: recover instead of showing a blank window.
    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) { retrySoon() }
    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) { retrySoon() }
    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) { web.load(URLRequest(url: uiURL)) }

    private var retrying = false
    func retrySoon() {
        if retrying { return }
        retrying = true
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) {
            self.retrying = false
            self.loadWhenReady()
        }
    }

    // Links to other sites open in the normal browser.
    func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        if let u = action.request.url, let h = u.host, h != "127.0.0.1", h != "localhost", u.scheme?.hasPrefix("http") == true {
            NSWorkspace.shared.open(u)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.addButton(withTitle: "OK")
        a.runModal()
        completionHandler()
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (Bool) -> Void) {
        let a = NSAlert()
        a.messageText = message
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        completionHandler(a.runModal() == .alertFirstButtonReturn)
    }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
        let a = NSAlert()
        a.messageText = prompt
        a.addButton(withTitle: "OK")
        a.addButton(withTitle: "Cancel")
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 280, height: 24))
        field.stringValue = defaultText ?? ""
        a.accessoryView = field
        a.window.initialFirstResponder = field
        completionHandler(a.runModal() == .alertFirstButtonReturn ? field.stringValue : nil)
    }

    // MARK: menu
    @objc func reload() { web.load(URLRequest(url: uiURL)) }
    @objc func restartEngine() {
        showMessage("Restarting engine…", spinner: true)
        DispatchQueue.global().async {
            Engine.restart()
            sleep(2)
            DispatchQueue.main.async { self.loadWhenReady() }
        }
    }
    @objc func openLogs() {
        let dir = NSHomeDirectory() + "/Library/Application Support/TVCast"
        NSWorkspace.shared.open(URL(fileURLWithPath: dir + "/tvcast.log"))
    }
    @objc func openHelp() { NSWorkspace.shared.open(URL(string: "https://github.com/TANMAYJAIN18/tvcast#readme")!) }

    func buildMenu() {
        let main = NSMenu()
        func add(_ title: String, _ items: [NSMenuItem]) {
            let top = NSMenuItem()
            let m = NSMenu(title: title)
            items.forEach { m.addItem($0) }
            top.submenu = m
            main.addItem(top)
        }
        func item(_ t: String, _ a: Selector?, _ k: String = "", target: AnyObject? = nil) -> NSMenuItem {
            let i = NSMenuItem(title: t, action: a, keyEquivalent: k)
            i.target = target
            return i
        }
        add("TV Cast", [
            item("About TV Cast", #selector(NSApplication.orderFrontStandardAboutPanel(_:))),
            .separator(),
            item("Restart Engine", #selector(restartEngine), target: self),
            item("Show Log", #selector(openLogs), target: self),
            .separator(),
            item("Hide TV Cast", #selector(NSApplication.hide(_:)), "h"),
            .separator(),
            item("Quit TV Cast", #selector(NSApplication.terminate(_:)), "q"),
        ])
        add("Edit", [
            item("Undo", Selector(("undo:")), "z"), item("Redo", Selector(("redo:")), "Z"), .separator(),
            item("Cut", #selector(NSText.cut(_:)), "x"), item("Copy", #selector(NSText.copy(_:)), "c"),
            item("Paste", #selector(NSText.paste(_:)), "v"), item("Select All", #selector(NSText.selectAll(_:)), "a"),
        ])
        add("View", [item("Reload", #selector(reload), "r", target: self),
                     item("Enter Full Screen", #selector(NSWindow.toggleFullScreen(_:)), "f")])
        add("Window", [item("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m"),
                       item("Close", #selector(NSWindow.performClose(_:)), "w")])
        add("Help", [item("TV Cast Help", #selector(openHelp), target: self)])
        NSApp.mainMenu = main
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
