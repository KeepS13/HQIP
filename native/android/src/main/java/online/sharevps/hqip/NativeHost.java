package online.sharevps.hqip;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import java.lang.ref.WeakReference;
import java.net.InetAddress;
import java.nio.ByteBuffer;
import java.security.KeyStore;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;
import mobile.Callbacks;
import mobile.Mobile;

/** One application-scoped controller. No Activity or credential is kept by Godot. */
final class NativeHost implements Callbacks {
    private static NativeHost instance;
    final mobile.Runtime runtime;
    final Context context;
    final ConnectivityManager connectivity;
    private WeakReference<Activity> activity;
    private final SecretKey key;

    static synchronized NativeHost get(Activity activity) throws Exception {
        if (instance == null) instance = new NativeHost(activity);
        instance.activity = new WeakReference<>(activity);
        return instance;
    }
    static void disconnect() {
        NativeHost host = instance;
        if (host != null) new Thread(() -> host.runtime.disconnect(), "hqip-disconnect").start();
    }
    private NativeHost(Activity activity) throws Exception {
        android.util.Log.i("HQIPStartup", "Preparing private storage");
        context = activity.getApplicationContext();
        this.activity = new WeakReference<>(activity);
        connectivity = context.getSystemService(ConnectivityManager.class);
        KeyStore store = KeyStore.getInstance("AndroidKeyStore"); store.load(null);
        String alias = "hqip-account-v1";
        if (!store.containsAlias(alias)) {
            KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore");
            generator.init(new KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());
            generator.generateKey();
        }
        key = (SecretKey) store.getKey(alias, null);
        android.util.Log.i("HQIPStartup", "Starting native runtime");
        runtime = Mobile.newRuntime(new java.io.File(context.getFilesDir(), "hqip").getAbsolutePath(), this);
        android.util.Log.i("HQIPStartup", "Native runtime ready");
        connectivity.registerNetworkCallback(new NetworkRequest.Builder().addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN).build(), new ConnectivityManager.NetworkCallback() {
                @Override public void onAvailable(Network network) { runtime.networkChanged(); }
                @Override public void onLost(Network network) { runtime.networkChanged(); }
                @Override public void onLinkPropertiesChanged(Network network, android.net.LinkProperties properties) { runtime.networkChanged(); }
                // Validation/transport changes can change the preferred physical
                // egress. Go deduplicates equal snapshots before resetting sockets.
                @Override public void onCapabilitiesChanged(Network network, NetworkCapabilities capabilities) { runtime.networkChanged(); }
            });
    }
    Network directNetwork() throws Exception {
        Network active = connectivity.getActiveNetwork();
        if (usable(active)) return active;
        for (Network network : connectivity.getAllNetworks()) if (usable(network)) return network;
        throw new java.io.IOException("No physical network");
    }
    private boolean usable(Network network) {
        NetworkCapabilities caps = network == null ? null : connectivity.getNetworkCapabilities(network);
        return caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN);
    }
    @Override public void protect(int fd) throws Exception {
        TunnelService service = TunnelService.current;
        if (service != null && !service.protect(fd)) throw new java.io.IOException("Socket protection rejected");
        try (ParcelFileDescriptor duplicate = ParcelFileDescriptor.fromFd(fd)) { directNetwork().bindSocket(duplicate.getFileDescriptor()); }
    }
    @Override public int openTun(String raw) throws Exception {
        TunnelService service = TunnelService.current;
        if (service == null) throw new IllegalStateException("VPN service missing");
        return service.open(raw);
    }
    @Override public void closeTun() throws Exception { TunnelService service = TunnelService.current; if (service != null) service.closeTunnel(); }
    @Override public byte[] encrypt(byte[] data) throws Exception {
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.ENCRYPT_MODE, key);
        byte[] iv = cipher.getIV(), encrypted = cipher.doFinal(data);
        return ByteBuffer.allocate(1 + iv.length + encrypted.length).put((byte) iv.length).put(iv).put(encrypted).array();
    }
    @Override public byte[] decrypt(byte[] data) throws Exception {
        ByteBuffer buffer = ByteBuffer.wrap(data); int length = Byte.toUnsignedInt(buffer.get());
        if (length != 12 || buffer.remaining() < 28) throw new java.io.IOException("Invalid encrypted account");
        byte[] iv = new byte[length]; buffer.get(iv); byte[] encrypted = new byte[buffer.remaining()]; buffer.get(encrypted);
        Cipher cipher = Cipher.getInstance("AES/GCM/NoPadding"); cipher.init(Cipher.DECRYPT_MODE, key, new GCMParameterSpec(128, iv));
        return cipher.doFinal(encrypted);
    }
    @Override public void openURL(String url) throws Exception {
        Activity current = activity.get();
        if (current == null || current.isFinishing()) throw new IllegalStateException("Open HQIP before browser login");
        current.runOnUiThread(() -> current.startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(url))));
    }
    @Override public String lookup(String domain) throws Exception {
        StringBuilder result = new StringBuilder();
        for (InetAddress address : directNetwork().getAllByName(domain)) { if (result.length() > 0) result.append('\n'); result.append(address.getHostAddress()); }
        return result.toString();
    }
    @Override public String interfaces() throws Exception { return NetworkSnapshot.read(this); }
    @Override public void installAPK(String path) throws Exception {
        Activity current = activity.get();
        if (current == null) throw new IllegalStateException("Open HQIP before installing");
        if (!context.getPackageManager().canRequestPackageInstalls()) {
            current.runOnUiThread(() -> current.startActivity(new Intent(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:" + context.getPackageName()))));
            throw new IllegalStateException("Install permission required");
        }
        java.io.File file = new java.io.File(path).getCanonicalFile();
        java.io.File updates = new java.io.File(context.getFilesDir(), "hqip/updates").getCanonicalFile();
        if (!file.getParentFile().equals(updates) || !file.getName().endsWith(".apk")) throw new SecurityException("Invalid update path");
        Uri uri = androidx.core.content.FileProvider.getUriForFile(context, "online.sharevps.hqip.updates", file);
        current.runOnUiThread(() -> current.startActivity(new Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive").addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)));
    }
}
