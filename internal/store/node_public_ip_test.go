package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/KeepS13/HQIP/internal/model"
)

func TestNodePublicIPTransaction(t *testing.T) {
	s, _ := fixture(t)
	v, err := s.CreateServer(model.Server{Name: "manual-ip", SSHHost: "8.8.8.8", HostKey: "pinned", MachineID: "verified"})
	if err != nil {
		t.Fatal(err)
	}
	create := func(ip, domain string) (model.Node, error) {
		return s.CreateNode(model.Node{ServerID: v.ID, PublicIP: ip, Name: domain, Domain: domain, Port: 443, SSHHost: "spoofed"}, "secret")
	}
	a, err := s.CreateAddress(v.ID, "8.8.4.4")
	if err != nil {
		t.Fatal(err)
	}
	n, err := create(" 8.8.4.4 ", "reuse.example.com")
	if err != nil || n.AddressID != a.ID || n.PublicIP != a.IP || n.SSHHost != v.SSHHost || n.HostKey != v.HostKey || n.QueuedJobID == 0 {
		t.Fatalf("did not reuse authoritative address/server: %+v %v", n, err)
	}
	if _, err := create(a.IP, "duplicate.example.com"); !errors.Is(err, ErrAddressInUse) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.DB.Exec("UPDATE nodes SET lifecycle='retiring' WHERE id=?", n.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := create(a.IP, "retiring.example.com"); !errors.Is(err, ErrAddressInUse) {
		t.Fatalf("retiring: %v", err)
	}
	if _, err := create("1.1.1.1", n.Domain); err == nil {
		t.Fatal("duplicate domain accepted")
	}
	for _, ip := range []string{"192.168.1.1", "203.0.113.1", "::ffff:8.8.8.8", "1.2.3.256"} {
		if _, err := create(ip, "invalid.example.com"); err == nil {
			t.Fatalf("accepted %q", ip)
		}
	}
	addresses, err := s.Addresses(v.ID)
	if err != nil || len(addresses) != 1 {
		t.Fatalf("failed create leaked address: %+v %v", addresses, err)
	}
	var audit int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM audit WHERE action='server.address.create'").Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("failed create leaked audit: %d %v", audit, err)
	}
	n2, err := create("1.1.1.1", "new.example.com")
	if err != nil || n2.AddressID == "" || n2.AddressID == a.ID || n2.AuthPort == n.AuthPort {
		t.Fatalf("new address/ports: %+v %v", n2, err)
	}
	if _, err := s.CreateNode(model.Node{ServerID: v.ID, AddressID: a.ID, PublicIP: "9.9.9.9", Name: "mismatch", Domain: "mismatch.example.com", Port: 443}, "secret"); err == nil {
		t.Fatal("mismatched ID/IP accepted")
	}
	if _, err := s.CreateNode(model.Node{ServerID: "missing", PublicIP: "9.9.9.9", Name: "missing", Domain: "missing.example.com", Port: 443}, "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server: %v", err)
	}
	unused, err := s.CreateAddress(v.ID, "9.9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAddress(v.ID, unused.ID); err != nil {
		t.Fatal(err)
	}
	n3, err := create(unused.IP, "readded.example.com")
	if err != nil || n3.AddressID == unused.ID {
		t.Fatalf("reused deleted address: %+v %v", n3, err)
	}
}

func TestNodePublicIPConcurrentCreation(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			s, _ := fixture(t)
			v, err := s.CreateServer(model.Server{Name: "concurrent", SSHHost: "8.8.8.8"})
			if err != nil {
				t.Fatal(err)
			}
			if registered {
				if _, err := s.CreateAddress(v.ID, "1.1.1.1"); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			start := make(chan struct{})
			results := make(chan error, 6)
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					_, err := s.CreateNode(model.Node{ServerID: v.ID, PublicIP: "1.1.1.1", Name: "concurrent", Domain: fmt.Sprintf("n%d.example.com", i), Port: 443}, "secret")
					results <- err
				}(i)
			}
			close(start)
			wg.Wait()
			close(results)
			count := 0
			for err := range results {
				if err == nil {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("created %d nodes", count)
			}
			for _, table := range []string{"server_addresses", "nodes", "jobs"} {
				if err := s.DB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count=%d: %v", table, count, err)
				}
			}
		})
	}
}
