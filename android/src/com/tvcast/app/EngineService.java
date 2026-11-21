package com.tvcast.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import android.util.Log;

import java.io.File;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.util.Map;

/**
 * Runs the TV Cast engine (a native program bundled as libtvcast.so) and keeps it alive.
 * While a TV is playing or paused on one of our movies, holds a CPU wake lock and a
 * high-performance Wi-Fi lock so streaming never stutters with the screen off.
 */
public class EngineService extends Service {
    static final String TAG = "TVCast";
    static final String CHANNEL = "tvcast";
    static final int NOTE_ID = 1;

    private volatile boolean running = true;
    private volatile Process proc;
    private PowerManager.WakeLock wake;
    private WifiManager.WifiLock wifi;
    private boolean streaming = false;

    @Override
    public void onCreate() {
        super.onCreate();
        NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (Build.VERSION.SDK_INT >= 26 && nm != null) {
            NotificationChannel ch = new NotificationChannel(CHANNEL, "TV Cast", NotificationManager.IMPORTANCE_LOW);
            ch.setShowBadge(false);
            nm.createNotificationChannel(ch);
        }
        Notification n = note("Ready to cast");
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTE_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK);
        } else {
            startForeground(NOTE_ID, n);
        }
        PowerManager pm = (PowerManager) getSystemService(Context.POWER_SERVICE);
        wake = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "tvcast:streaming");
        wake.setReferenceCounted(false);
        WifiManager wm = (WifiManager) getApplicationContext().getSystemService(Context.WIFI_SERVICE);
        if (wm != null) {
            int mode = Build.VERSION.SDK_INT >= 29 ? WifiManager.WIFI_MODE_FULL_LOW_LATENCY : WifiManager.WIFI_MODE_FULL_HIGH_PERF;
            wifi = wm.createWifiLock(mode, "tvcast:streaming");
            wifi.setReferenceCounted(false);
        }
        new Thread(this::supervise, "tvcast-engine").start();
        new Thread(this::awakeLoop, "tvcast-awake").start();
    }

    private Notification note(String text) {
        Intent open = new Intent(this, MainActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0);
        PendingIntent pi = PendingIntent.getActivity(this, 0, open, flags);
        Notification.Builder b = Build.VERSION.SDK_INT >= 26 ? new Notification.Builder(this, CHANNEL) : new Notification.Builder(this);
        return b.setContentTitle("TV Cast")
                .setContentText(text)
                .setSmallIcon(android.R.drawable.ic_media_play)
                .setContentIntent(pi)
                .setOngoing(true)
                .build();
    }

    /** Starts the engine and restarts it if it ever stops. */
    private void supervise() {
        File bin = new File(getApplicationInfo().nativeLibraryDir, "libtvcast.so");
        int fails = 0;
        while (running) {
            long started = System.currentTimeMillis();
            try {
                ProcessBuilder pb = new ProcessBuilder(bin.getAbsolutePath(), "--background");
                Map<String, String> env = pb.environment();
                env.put("HOME", getFilesDir().getAbsolutePath());
                env.put("TMPDIR", getCacheDir().getAbsolutePath());
                env.put("SSL_CERT_DIR", "/system/etc/security/cacerts");
                pb.directory(getFilesDir());
                pb.redirectErrorStream(true);
                pb.redirectOutput(new File(getFilesDir(), "engine-console.log"));
                proc = pb.start();
                int rc = proc.waitFor();
                Log.w(TAG, "engine exited with " + rc);
            } catch (Exception e) {
                Log.e(TAG, "engine start failed", e);
            }
            if (!running) break;
            fails = (System.currentTimeMillis() - started < 10000) ? fails + 1 : 0;
            sleep(Math.min(2000L * Math.max(1, fails), 30000L));
        }
    }

    /** Mirrors the engine's "awake" state into wake + Wi-Fi locks. */
    private void awakeLoop() {
        while (running) {
            boolean active = false;
            try {
                HttpURLConnection c = (HttpURLConnection) new URL("http://127.0.0.1:8765/api/info").openConnection();
                c.setConnectTimeout(2000);
                c.setReadTimeout(5000);
                try (InputStream in = c.getInputStream()) {
                    byte[] buf = new byte[8192];
                    int n = in.read(buf);
                    active = n > 0 && new String(buf, 0, n, "UTF-8").contains("\"awake\":true");
                }
                c.disconnect();
            } catch (Exception ignored) {
            }
            setStreaming(active);
            sleep(active ? 10000 : 15000);
        }
    }

    private synchronized void setStreaming(boolean on) {
        if (on && streaming) {
            try { wake.acquire(6 * 60 * 60 * 1000L); } catch (Exception ignored) { } // refresh the safety cap
            return;
        }
        if (on == streaming) return;
        streaming = on;
        try {
            if (on) {
                wake.acquire(6 * 60 * 60 * 1000L); // safety cap; refreshed while streaming
                if (wifi != null) wifi.acquire();
            } else {
                if (wake.isHeld()) wake.release();
                if (wifi != null && wifi.isHeld()) wifi.release();
            }
        } catch (Exception e) {
            Log.w(TAG, "lock change failed", e);
        }
        NotificationManager nm = (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
        if (nm != null) nm.notify(NOTE_ID, note(on ? "Streaming to your TV" : "Ready to cast"));
    }

    private static void sleep(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) { }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        return START_STICKY;
    }

    @Override
    public void onDestroy() {
        running = false;
        Process p = proc;
        if (p != null) p.destroy();
        setStreaming(false);
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
