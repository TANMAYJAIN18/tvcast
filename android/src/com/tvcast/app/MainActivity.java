package com.tvcast.app;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.PowerManager;
import android.provider.Settings;
import android.view.View;
import android.view.Window;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.ArrayList;
import java.util.List;

/** TV Cast window: the engine's UI in a WebView, plus Android permissions and setup. */
public class MainActivity extends Activity {
    static final String UI = "http://127.0.0.1:8765/";
    private WebView web;
    private final Handler ui = new Handler(Looper.getMainLooper());
    private boolean loading = false;

    @Override
    protected void onCreate(Bundle saved) {
        super.onCreate(saved);
        Window w = getWindow();
        w.setStatusBarColor(Color.rgb(11, 12, 16));
        w.setNavigationBarColor(Color.rgb(11, 12, 16));

        Intent svc = new Intent(this, EngineService.class);
        if (Build.VERSION.SDK_INT >= 26) startForegroundService(svc); else startService(svc);

        web = new WebView(this);
        web.setBackgroundColor(Color.rgb(11, 12, 16));
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setCacheMode(WebSettings.LOAD_NO_CACHE);
        s.setAllowFileAccess(false);
        s.setAllowContentAccess(false);
        web.setWebChromeClient(new WebChromeClient());
        web.addJavascriptInterface(new Bridge(), "TVCastAndroid");
        web.setWebViewClient(new WebViewClient() {
            @Override
            public boolean shouldOverrideUrlLoading(WebView v, WebResourceRequest r) {
                Uri u = r.getUrl();
                if ("127.0.0.1".equals(u.getHost()) || "localhost".equals(u.getHost())) return false;
                try { startActivity(new Intent(Intent.ACTION_VIEW, u)); } catch (Exception ignored) { }
                return true;
            }

            @Override
            public void onReceivedError(WebView v, WebResourceRequest r, WebResourceError e) {
                if (r.isForMainFrame()) {
                    showMessage("Starting TV Cast…");
                    ui.postDelayed(MainActivity.this::loadWhenReady, 1500);
                }
            }
        });
        setContentView(web);
        showMessage("Starting TV Cast…");
        askPermissions();
        loadWhenReady();
    }

    private void showMessage(String text) {
        web.loadDataWithBaseURL(null, "<html><body style='margin:0;height:100vh;display:flex;align-items:center;"
                + "justify-content:center;background:#0b0c10;color:#9aa1b5;font-family:sans-serif;font-size:16px'>"
                + "<div style='text-align:center'><div style='width:30px;height:30px;margin:0 auto 14px;border:3px solid #333;"
                + "border-top-color:#5b8cff;border-radius:50%;animation:r .8s linear infinite'></div>" + text
                + "</div><style>@keyframes r{to{transform:rotate(360deg)}}</style></body></html>", "text/html", "utf-8", null);
    }

    private void loadWhenReady() {
        if (loading) return;
        loading = true;
        new Thread(() -> {
            boolean ok = false;
            for (int i = 0; i < 60 && !ok; i++) {
                ok = ping();
                if (!ok) try { Thread.sleep(250); } catch (InterruptedException ignored) { }
            }
            final boolean ready = ok;
            ui.post(() -> {
                loading = false;
                if (ready) web.loadUrl(UI);
                else {
                    showMessage("The TV Cast engine didn't start. Close the app and open it again.");
                    ui.postDelayed(this::loadWhenReady, 4000);
                }
            });
        }).start();
    }

    static boolean ping() {
        try {
            HttpURLConnection c = (HttpURLConnection) new URL(UI + "api/ping").openConnection();
            c.setConnectTimeout(700);
            c.setReadTimeout(1500);
            boolean ok = c.getResponseCode() == 200;
            c.disconnect();
            return ok;
        } catch (Exception e) {
            return false;
        }
    }

    // ---------------------------------------------------------------- permissions & setup

    private void askPermissions() {
        List<String> need = new ArrayList<>();
        if (Build.VERSION.SDK_INT >= 33) {
            need.add(Manifest.permission.READ_MEDIA_VIDEO);
            need.add(Manifest.permission.POST_NOTIFICATIONS);
        } else if (Build.VERSION.SDK_INT >= 23) {
            need.add(Manifest.permission.READ_EXTERNAL_STORAGE);
        }
        List<String> missing = new ArrayList<>();
        for (String p : need) {
            if (Build.VERSION.SDK_INT >= 23 && checkSelfPermission(p) != PackageManager.PERMISSION_GRANTED) missing.add(p);
        }
        if (!missing.isEmpty() && Build.VERSION.SDK_INT >= 23) {
            requestPermissions(missing.toArray(new String[0]), 1);
        } else {
            askBackground();
        }
    }

    @Override
    public void onRequestPermissionsResult(int code, String[] perms, int[] results) {
        // Videos are now readable: let the engine rescan, then refresh the screen.
        new Thread(() -> {
            post("api/rescan");
            ui.post(() -> { if (!loading) web.reload(); });
        }).start();
        askBackground();
    }

    /** Streaming must survive the screen turning off: ask to skip battery restrictions once. */
    private void askBackground() {
        if (Build.VERSION.SDK_INT < 23) return;
        PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
        if (pm != null && !pm.isIgnoringBatteryOptimizations(getPackageName())) {
            try {
                startActivity(new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                        Uri.parse("package:" + getPackageName())));
            } catch (Exception ignored) { }
        }
        SharedPreferences prefs = getSharedPreferences("tvcast", MODE_PRIVATE);
        if (prefs.getBoolean("bgTipShown", false)) return;
        String maker = Build.MANUFACTURER == null ? "" : Build.MANUFACTURER.toLowerCase();
        String tip;
        if (maker.contains("vivo") || maker.contains("iqoo")) {
            tip = "Vivo phones close apps in the background. So movies never stop when the screen is off:\n\n"
                + "App info → Battery → choose “Allow high background power consumption” (or “Don't restrict”).\n\n"
                + "Also lock TV Cast in Recents: open Recents, pull TV Cast down (lock icon).";
        } else if (maker.contains("samsung")) {
            tip = "So movies never stop when the screen is off:\n\nApp info → Battery → choose “Unrestricted”.\n\n"
                + "TV Cast will then never be put to sleep by Samsung's battery saver.";
        } else if (maker.contains("xiaomi") || maker.contains("oppo") || maker.contains("realme")
                || maker.contains("oneplus") || maker.contains("huawei") || maker.contains("honor")) {
            tip = "So movies never stop when the screen is off:\n\nApp info → Battery → allow background activity / “No restrictions”, and enable Autostart if shown.";
        } else {
            return;
        }
        prefs.edit().putBoolean("bgTipShown", true).apply();
        new AlertDialog.Builder(this, android.R.style.Theme_DeviceDefault_Dialog_Alert)
                .setTitle("One setting for smooth streaming")
                .setMessage(tip)
                .setPositiveButton("Open App info", (d, i) -> {
                    try {
                        startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                Uri.parse("package:" + getPackageName())));
                    } catch (Exception ignored) { }
                })
                .setNegativeButton("Later", null)
                .show();
    }

    /** Lets the page show a "fix background streaming" banner. Only our local UI is ever loaded. */
    final class Bridge {
        @JavascriptInterface
        public boolean backgroundAllowed() {
            if (Build.VERSION.SDK_INT < 23) return true;
            PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
            return pm == null || pm.isIgnoringBatteryOptimizations(getPackageName());
        }

        @JavascriptInterface
        public String maker() {
            return Build.MANUFACTURER == null ? "" : Build.MANUFACTURER.toLowerCase();
        }

        @JavascriptInterface
        public void fixBackground() {
            ui.post(() -> {
                try {
                    if (!backgroundAllowed()) {
                        startActivity(new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                                Uri.parse("package:" + getPackageName())));
                    } else {
                        startActivity(new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                                Uri.parse("package:" + getPackageName())));
                    }
                } catch (Exception ignored) { }
            });
        }
    }

    static void post(String path) {
        try {
            HttpURLConnection c = (HttpURLConnection) new URL(UI + path).openConnection();
            c.setRequestMethod("POST");
            c.setDoOutput(true);
            c.setConnectTimeout(2000);
            c.setReadTimeout(120000);
            c.setRequestProperty("Content-Type", "application/json");
            try (OutputStream o = c.getOutputStream()) { o.write("{}".getBytes("UTF-8")); }
            c.getResponseCode();
            c.disconnect();
        } catch (Exception ignored) { }
    }

    @Override
    public void onBackPressed() {
        // Back closes panels in the page first (Escape), then leaves the app (engine keeps running).
        web.evaluateJavascript("(function(){var d=document.getElementById('drawer');"
                + "if(d&&d.classList.contains('on')){closeDrawer();return 1}return 0})()", v -> {
            if (!"1".equals(v)) moveTaskToBack(true);
        });
    }

    @Override
    protected void onResume() {
        super.onResume();
        web.onResume();
    }

    @Override
    protected void onPause() {
        web.onPause();
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        if (web != null) {
            web.setVisibility(View.GONE);
            web.destroy();
        }
        super.onDestroy();
    }
}
