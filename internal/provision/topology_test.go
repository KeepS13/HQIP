package provision

import (
	"github.com/KeepS13/HQIP/internal/model"
	"testing"
)

func TestMigratedControlKeepsCertificateRoute(t *testing.T) {
	for _, node := range []model.Node{{SharedHost: true}, {ACMEViaCaddy: true}} {
		local, route := deploymentTopology(node, "same", "same", "windows")
		if local || !route {
			t.Fatal("Windows migration lost Caddy route or looked like same-host verification")
		}
		local, route = deploymentTopology(node, "other", "same", "linux")
		if local || !route {
			t.Fatal("Linux migration lost existing Caddy route")
		}
	}
	local, route := deploymentTopology(model.Node{}, "same\n", "same", "linux")
	if !local || !route {
		t.Fatal("actual shared Linux host not detected")
	}
	local, route = deploymentTopology(model.Node{}, "", "remote", "windows")
	if local || route {
		t.Fatal("new remote node incorrectly shared")
	}
}

func TestProbeArtifactMatchesHost(t *testing.T) {
	for _, os := range []string{"linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			got, err := probeArtifact(os, arch)
			want := "hysteria-" + os + "-" + arch
			if os == "windows" {
				want += ".exe"
			}
			if err != nil || got != want {
				t.Fatalf("%s/%s: %s %v", os, arch, got, err)
			}
		}
	}
	if _, err := probeArtifact("darwin", "amd64"); err == nil {
		t.Fatal("unsupported host accepted")
	}
}
