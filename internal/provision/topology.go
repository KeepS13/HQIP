package provision

import (
	"errors"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
)

// An existing ACME route survives moving the controller to another computer.
// SharedHost is read only as the pre-migration certificate topology hint.
func deploymentTopology(node model.Node, localID, remoteID, goos string) (controlOnNode, acmeViaCaddy bool) {
	controlOnNode = goos == "linux" && strings.TrimSpace(localID) != "" && strings.TrimSpace(localID) == strings.TrimSpace(remoteID)
	acmeViaCaddy = controlOnNode || node.ACMEViaCaddy || node.SharedHost
	return
}

func probeArtifact(goos, arch string) (string, error) {
	if (goos != "linux" && goos != "windows") || (arch != "amd64" && arch != "arm64") {
		return "", errors.New("自动探测仅支持 Windows/Linux amd64/arm64")
	}
	name := "hysteria-" + goos + "-" + arch
	if goos == "windows" {
		name += ".exe"
	}
	return name, nil
}
