package online.sharevps.hqip.preview;

import android.content.Intent;
import android.net.VpnService;
import android.os.Handler;
import android.os.Looper;
import androidx.annotation.NonNull;
import io.flutter.embedding.android.FlutterActivity;
import io.flutter.embedding.engine.FlutterEngine;
import io.flutter.plugin.common.EventChannel;
import io.flutter.plugin.common.MethodCall;
import io.flutter.plugin.common.MethodChannel;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicLong;
import org.json.JSONObject;

/** Only secret-free JSON crosses this boundary. The service owns the VPN lifetime. */
public final class MainActivity extends FlutterActivity {
    private final Handler ui = new Handler(Looper.getMainLooper());
    private final ThreadPoolExecutor commands = new ThreadPoolExecutor(4, 4, 30, TimeUnit.SECONDS, new ArrayBlockingQueue<>(16));
    private final ThreadPoolExecutor urgent = new ThreadPoolExecutor(2, 2, 30, TimeUnit.SECONDS, new ArrayBlockingQueue<>(4));
    private final AtomicLong intentRevision = new AtomicLong();
    private EventChannel.EventSink events;
    private volatile boolean foreground, destroyed;
    private boolean polling;
    private JSONObject pendingConnect;
    private MethodChannel.Result pendingResult;

    @Override public void configureFlutterEngine(@NonNull FlutterEngine engine) {
        super.configureFlutterEngine(engine);
        new MethodChannel(engine.getDartExecutor().getBinaryMessenger(), "hqip/preview/runtime").setMethodCallHandler(this::call);
        new EventChannel(engine.getDartExecutor().getBinaryMessenger(), "hqip/preview/state").setStreamHandler(new EventChannel.StreamHandler() {
            public void onListen(Object args, EventChannel.EventSink sink) { events = sink; poll(); }
            public void onCancel(Object args) { events = null; }
        });
    }
    private void call(MethodCall call, MethodChannel.Result result) {
        try {
            if (!call.method.equals("call") || !(call.arguments instanceof String) || ((String)call.arguments).length() > 65536) throw new IllegalArgumentException("Invalid command");
            JSONObject request = new JSONObject((String)call.arguments);
            String method = request.getString("method");
            if (method.equals("uiReady")) { result.success(reply(request, null)); return; }
            if (method.equals("disconnect") || method.equals("quitApp") || method.equals("logout")) {
                intentRevision.incrementAndGet(); cancelPermission();
            }
            if (method.equals("connect")) {
                if (pendingResult != null) { result.success(reply(request, "VPN 授权正在等待确认")); return; }
                Intent consent = VpnService.prepare(this);
                if (consent != null) {
                    pendingConnect = request; pendingResult = result;
                    startActivityForResult(consent, 7104); return;
                }
            }
            dispatch(request, result);
        } catch (Exception e) { result.error("command", "无法执行操作，请重试", null); }
    }
    private void dispatch(JSONObject request, MethodChannel.Result result) {
        String method = request.optString("method");
        long revision = intentRevision.get();
        ThreadPoolExecutor pool = method.equals("disconnect") || method.equals("cancelLogin") || method.equals("quitApp") ? urgent : commands;
        try { pool.execute(() -> {
            String response;
            try {
                NativeHost host = NativeHost.get(this);
                if (method.equals("connect")) {
                    if (revision != intentRevision.get()) throw new IllegalStateException("Cancelled");
                    startForegroundService(new Intent(this, TunnelService.class));
                    if (!TunnelService.awaitReady(5000) || revision != intentRevision.get()) throw new IllegalStateException("VPN service unavailable");
                }
                if (method.equals("quitApp")) {
                    host.runtime.disconnect(); stopService(new Intent(this, TunnelService.class));
                    response = reply(request, null);
                } else {
                    response = host.runtime.call(request.toString());
                    if (method.equals("disconnect") || method.equals("logout") ||
                        (method.equals("connect") && new JSONObject(response).has("error"))) {
                        stopService(new Intent(this, TunnelService.class));
                    }
                }
            } catch (Exception e) { response = reply(request, "操作未完成，请确认 VPN 授权及网络后重试"); }
            final String value = response;
            ui.post(() -> {
                if (!destroyed) { result.success(value); if (method.equals("quitApp")) finishAndRemoveTask(); }
            });
        }); } catch (java.util.concurrent.RejectedExecutionException e) { result.success(reply(request, "操作较多，请稍后重试")); }
    }
    private static String reply(JSONObject request, String error) {
        JSONObject result = new JSONObject();
        try { result.put("id", request.optLong("id")); if (error != null) result.put("error", error); else result.put("result", new JSONObject()); } catch (Exception ignored) { }
        return result.toString();
    }
    private void cancelPermission() {
        if (pendingResult != null) pendingResult.success(reply(pendingConnect, "VPN 授权已取消"));
        pendingConnect = null; pendingResult = null;
    }
    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == 7104 && pendingResult != null) {
            JSONObject request = pendingConnect; MethodChannel.Result result = pendingResult;
            pendingConnect = null; pendingResult = null;
            if (resultCode == RESULT_OK) dispatch(request, result); else result.success(reply(request, "VPN 授权已取消"));
        }
    }
    private void poll() {
        if (events == null || !foreground || destroyed || polling) return;
        polling = true;
        try { commands.execute(() -> {
            String state = null;
            try { state = new JSONObject(NativeHost.get(this).runtime.call("{\"id\":1,\"method\":\"state\"}")).getJSONObject("result").toString(); } catch (Exception ignored) { }
            final String value = state;
            ui.post(() -> {
                polling = false;
                if (events != null && foreground && !destroyed) {
                    if (value != null) events.success(value);
                    ui.postDelayed(this::poll, 1000);
                }
            });
        }); } catch (java.util.concurrent.RejectedExecutionException e) { polling = false; ui.postDelayed(this::poll, 1000); }
    }
    @Override protected void onResume() { super.onResume(); foreground = true; poll(); }
    @Override protected void onPause() { foreground = false; super.onPause(); }
    @Override protected void onDestroy() {
        cancelPermission(); destroyed = true; events = null;
        ui.removeCallbacksAndMessages(null); commands.shutdown(); urgent.shutdown();
        super.onDestroy();
    }
}
