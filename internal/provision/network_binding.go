package provision

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
)

// nodeBindingIP keeps advertised identity separate from the kernel's address.
// A direct address wins. NAT requires both a bound unicast source and its exact
// observed public exit; an empty/failed observation never authorizes a fallback.
// These observations belong to this SSH preflight, not the business database.
func nodeBindingIP(publicIP string, inv hostInventory) string {
	if slices.Contains(inv.BoundIPs, publicIP) {
		return publicIP
	}
	if validBindingIP(inv.RouteIP) && slices.Contains(inv.BoundIPs, inv.RouteIP) && inv.ExitIP == publicIP {
		return inv.RouteIP
	}
	return ""
}

func validBindingIP(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && ip.To4() != nil && ip.IsGlobalUnicast()
}

// Recheck under the host operation lock before stopping or modifying services.
// The source is local even when the required externally observed exit is NATed.
func (r *Runner) verifyNetworkBinding(ctx context.Context, rm *remote, bindIP, publicIP string, log LogFunc) error {
	if bindIP != publicIP {
		release, err := r.AgentRelease()
		if err != nil || release.NodeNetworkVersion != 1 {
			return errors.New("当前 Agent 发布版本不支持 NAT 节点，请先更新控制端发布包")
		}
		log("preflight", "已验证 NAT 映射，使用本机地址监听并保留公网节点地址")
	}
	out, err := rm.run(ctx, "curl -4 --noproxy '*' --fail --silent --show-error --connect-timeout 5 --max-time 20 --interface "+shellQuote(bindIP)+" https://api.ipify.org", nil)
	if err != nil || strings.TrimSpace(out) != publicIP {
		return errors.New("节点本机地址的公网出口验证失败，未修改网卡或路由")
	}
	return nil
}
