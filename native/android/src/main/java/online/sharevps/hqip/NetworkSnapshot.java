package online.sharevps.hqip;

import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import java.net.NetworkInterface;
import org.json.JSONArray;
import org.json.JSONObject;

/** Android network APIs remain available where Go's direct netlink access is restricted. */
final class NetworkSnapshot {
    static String read(NativeHost host) throws Exception {
        Network selected;
        try { selected = host.directNetwork(); } catch (Exception offline) { return "[]"; }
        JSONArray result = new JSONArray();
        for (Network network : host.connectivity.getAllNetworks()) {
            NetworkCapabilities capabilities = host.connectivity.getNetworkCapabilities(network);
            LinkProperties properties = host.connectivity.getLinkProperties(network);
            if (capabilities == null || properties == null || !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) continue;
            String name = properties.getInterfaceName();
            NetworkInterface iface = name == null ? null : NetworkInterface.getByName(name);
            if (iface == null) continue;
            JSONArray addresses = new JSONArray(), dns = new JSONArray();
            for (android.net.LinkAddress address : properties.getLinkAddresses()) addresses.put(address.getAddress().getHostAddress() + "/" + address.getPrefixLength());
            for (java.net.InetAddress address : properties.getDnsServers()) dns.put(address.getHostAddress());
            result.put(new JSONObject().put("networkId", network.getNetworkHandle()).put("name", name).put("index", iface.getIndex()).put("mtu", Math.max(1280, iface.getMTU()))
                .put("addresses", addresses).put("dns", dns).put("default", network.equals(selected))
                .put("metered", !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)));
        }
        return result.toString();
    }
}
