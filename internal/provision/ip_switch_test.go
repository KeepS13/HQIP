package provision

import (
	"github.com/KeepS13/HQIP/internal/model"
	"testing"
)

func TestIPSwitchOwnershipIsLimitedToSameInstance(t *testing.T) {
	r := New(Options{ControllerID: "controller"})
	n := validNode()
	inv := inventoryFixture()
	oldIP := n.PublicIP
	n.PublicIP = "1.1.1.1"
	inv.BoundIPs = []string{n.PublicIP}
	owner := instanceOwner{NodeID: n.ID, ControllerID: "controller", MachineID: inv.MachineID, PublicIP: oldIP, InstallationVersion: installationVersion}
	inv.Owner = &owner
	if r.assess(n, inv).CanInstall {
		t.Fatal("IP change accepted without durable intent")
	}
	n.IPSwitch = &model.NodeIPSwitch{FromIP: oldIP, FromAddressID: "old-address"}
	if pre := r.assess(n, inv); !pre.CanInstall || !pre.ExistingManaged {
		t.Fatal("own old IP rejected", pre)
	}
	for _, mutate := range []func(*instanceOwner){
		func(o *instanceOwner) { o.NodeID = "foreign" },
		func(o *instanceOwner) { o.ControllerID = "foreign" },
		func(o *instanceOwner) { o.MachineID = "foreign" },
		func(o *instanceOwner) { o.PublicIP = "9.9.9.9" },
		func(o *instanceOwner) { o.InstallationVersion = 1 },
	} {
		copy := owner
		mutate(&copy)
		inv.Owner = &copy
		if pre := r.assess(n, inv); pre.CanInstall || pre.ExistingManaged {
			t.Fatal("foreign owner accepted", copy)
		}
	}
	copy := owner
	copy.PublicIP = n.PublicIP
	inv.Owner = &copy
	if !r.assess(n, inv).CanInstall {
		t.Fatal("retry after owner replacement rejected")
	}
	inv.BoundIPs = []string{oldIP}
	if r.assess(n, inv).CanInstall {
		t.Fatal("unbound target IP accepted")
	}
	inv.Owner = nil
	inv.BoundIPs = []string{n.PublicIP}
	if r.assess(n, inv).CanInstall {
		t.Fatal("missing owner accepted for migration")
	}
}
