package online.sharevps.hqip;

import android.app.Activity;
import android.content.Intent;
import android.net.VpnService;
import org.godotengine.godot.Godot;
import org.godotengine.godot.plugin.GodotPlugin;
import org.godotengine.godot.plugin.UsedByGodot;
import org.json.JSONObject;
import java.util.concurrent.ArrayBlockingQueue;
import java.util.concurrent.ThreadPoolExecutor;
import java.util.concurrent.TimeUnit;

/** Calls never block the renderer; cancellation has a separate worker from login. */
public final class RuntimePlugin extends GodotPlugin {
    private static final int VPN_PERMISSION = 7103;
    private final ArrayBlockingQueue<String> replies = new ArrayBlockingQueue<>(64);
    private final ThreadPoolExecutor work = new ThreadPoolExecutor(4, 4, 30, TimeUnit.SECONDS, new ArrayBlockingQueue<>(32));
    private String pendingConnect;

    public RuntimePlugin(Godot godot) { super(godot); }
    @Override public String getPluginName() { return "HQIPRuntime"; }

    @UsedByGodot public String initialize() {
        // Godot invokes this on its native renderer thread. Loading gomobile
        // there can block the first frame on Android runtimes. The first state
        // command constructs the host on our Java executor and reports errors
        // through the same correlated response channel as other commands.
        return "";
    }
    @UsedByGodot public String poll() { String value = replies.poll(); return value == null ? "" : value; }

    /** Back from the root page backgrounds the Activity; the VPN service owns
     * connectivity. Back inside Godot's drawer/modal is handled by the shell. */
    @UsedByGodot public void background() {
        runOnUiThread(() -> { Activity activity = getActivity(); if (activity != null) activity.moveTaskToBack(true); });
    }

    @UsedByGodot public void submit(String raw) {
        if (raw.length() > 65536) return;
        try {
            JSONObject request = new JSONObject(raw);
            if ("connect".equals(request.optString("method"))) {
                runOnUiThread(() -> requestVPN(raw));
                return;
            }
            execute(raw);
        } catch (Exception error) { reject(raw, "客户端命令无效"); }
    }

    private void requestVPN(String raw) {
        if (pendingConnect != null) { reject(raw, "正在等待系统 VPN 授权"); return; }
        Intent intent = VpnService.prepare(getActivity());
        if (intent != null) {
            pendingConnect = raw;
            getActivity().startActivityForResult(intent, VPN_PERMISSION);
        } else startVPN(raw);
    }

    @Override public void onMainActivityResult(int request, int result, Intent data) {
        if (request != VPN_PERMISSION || pendingConnect == null) return;
        String raw = pendingConnect; pendingConnect = null;
        if (result == Activity.RESULT_OK) startVPN(raw);
        else reject(raw, "未授予系统 VPN 权限，尚未连接");
    }

    private void startVPN(String raw) {
        try {
            getActivity().startForegroundService(new Intent(getActivity(), TunnelService.class));
            work.execute(() -> {
                try {
                    if (!TunnelService.awaitReady(5000)) { reject(raw, "VPN 服务启动超时"); return; }
                    complete(raw, NativeHost.get(getActivity()).runtime.call(raw));
                } catch (Exception error) { reject(raw, "VPN 服务启动失败"); }
            });
        } catch (Exception error) { reject(raw, "系统未允许启动 VPN 服务"); }
    }

    private void execute(String raw) {
        try {
            work.execute(() -> {
                try { complete(raw, NativeHost.get(getActivity()).runtime.call(raw)); }
                catch (Exception error) { reject(raw, "客户端操作失败，请稍后重试"); }
            });
        } catch (Exception full) { reject(raw, "操作过于频繁，请稍后重试"); }
    }
    private void reject(String raw, String message) {
        try {
            long id = new JSONObject(raw).optLong("id");
            replies.offer(new JSONObject().put("id", id).put("error", message).toString());
        } catch (Exception ignored) { /* Invalid requests have no usable correlation ID. */ }
    }
    /** Stop the foreground service after explicit termination or failed connect.
     * Core.Stop also runs during node switches, so it must not stop the service.
     */
    private void complete(String raw, String response) throws Exception {
        String method = new JSONObject(raw).optString("method");
        boolean failed = !new JSONObject(response).optString("error").isEmpty();
        if ((!failed && (method.equals("disconnect") || method.equals("logout"))) || (failed && method.equals("connect"))) {
            runOnUiThread(() -> getActivity().stopService(new Intent(getActivity(), TunnelService.class)));
        }
        replies.offer(response);
    }
    @Override public void onMainDestroy() {
        pendingConnect = null;
        work.shutdown();
        // The application host is retained by VpnService while VPN is active.
    }
}
