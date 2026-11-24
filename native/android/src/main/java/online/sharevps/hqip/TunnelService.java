package online.sharevps.hqip;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.net.VpnService;
import android.os.ParcelFileDescriptor;
import org.json.JSONArray;
import org.json.JSONObject;

/** Owns the original TUN descriptor; libbox duplicates it and owns its duplicate. */
public final class TunnelService extends VpnService {
    private static final Object READY = new Object();
    static volatile TunnelService current;
    private ParcelFileDescriptor tunnel;

    @Override public void onCreate() {
        super.onCreate();
        NotificationManager manager = getSystemService(NotificationManager.class);
        manager.createNotificationChannel(new NotificationChannel("vpn", "VPN 连接", NotificationManager.IMPORTANCE_LOW));
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        Notification notification = new Notification.Builder(this, "vpn").setContentTitle("HQIP 私人网络")
            .setContentText("VPN 正在运行，可在应用内断开").setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(open).setOngoing(true).build();
        startForeground(7103, notification);
        synchronized (READY) { current = this; READY.notifyAll(); }
    }
    @Override public int onStartCommand(Intent intent, int flags, int id) { return START_NOT_STICKY; }
    static boolean awaitReady(long millis) throws InterruptedException {
        long until = android.os.SystemClock.elapsedRealtime() + millis;
        synchronized (READY) {
            while (current == null) {
                long left = until - android.os.SystemClock.elapsedRealtime();
                if (left <= 0) return false;
                READY.wait(left);
            }
            return true;
        }
    }

    synchronized int open(String raw) throws Exception {
        closeTunnel();
        if (VpnService.prepare(this) != null) throw new IllegalStateException("VPN permission revoked");
        JSONObject config = new JSONObject(raw);
        Builder builder = new Builder().setSession("HQIP").setMtu(Math.max(1280, config.optInt("mtu", 1500)));
        // Excluding only our UID keeps account requests, browser callback and
        // updater outside TUN. Upstream sockets are additionally protected.
        builder.addDisallowedApplication(getPackageName());
        addPrefixes(builder, config.getJSONArray("addresses"), false);
        JSONArray routes = config.getJSONArray("routes");
        if (routes.length() == 0) { builder.addRoute("0.0.0.0", 0); builder.addRoute("::", 0); }
        else addPrefixes(builder, routes, true);
        JSONArray servers = config.getJSONArray("dns");
        for (int i = 0; i < servers.length(); i++) builder.addDnsServer(servers.getString(i));
        if (android.os.Build.VERSION.SDK_INT >= 29) builder.setMetered(false);
        tunnel = builder.establish();
        if (tunnel == null) throw new IllegalStateException("VPN establishment rejected");
        return tunnel.getFd();
    }
    private static void addPrefixes(Builder builder, JSONArray list, boolean routes) throws Exception {
        for (int i = 0; i < list.length(); i++) {
            String[] parts = list.getString(i).split("/");
            if (routes) builder.addRoute(parts[0], Integer.parseInt(parts[1]));
            else builder.addAddress(parts[0], Integer.parseInt(parts[1]));
        }
    }
    synchronized void closeTunnel() throws Exception { if (tunnel != null) { tunnel.close(); tunnel = null; } }
    @Override public void onRevoke() {
        NativeHost.disconnect();
        stopSelf();
    }
    @Override public void onDestroy() {
        synchronized (READY) { current = null; }
        NativeHost.disconnect();
        try { closeTunnel(); } catch (Exception ignored) { }
        super.onDestroy();
    }
}
