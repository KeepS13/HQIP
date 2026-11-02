// hqip-fixture prepares an explicitly marked second endpoint for acceptance.
// It never calls an installer, SSH, systemctl, or the running desktop client.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

type fixture struct {
	Marker        string `json:"marker"`
	NodeA         string `json:"nodeA"`
	NodeID        string `json:"nodeId"`
	DeviceID      string `json:"fixtureDeviceId"`
	Token         string `json:"token"`
	ControlURL    string `json:"controlUrl"`
	StatsSecret   string `json:"statsSecret"`
	PublicIP      string `json:"publicIp"`
	Domain        string `json:"domain"`
	Port          int    `json:"port"`
	ProbePassword string `json:"probePassword"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "cleanup") {
		return errors.New("usage: hqip-fixture prepare|cleanup --controller-config PATH [--node-a ID]")
	}
	flags := flag.NewFlagSet("fixture", flag.ContinueOnError)
	configPath := flags.String("controller-config", ".local/controller/controller.json", "private local controller configuration")
	nodeA := flags.String("node-a", "", "existing node A id; required for prepare")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	var config struct {
		DataDir   string `json:"dataDir"`
		StateDir  string `json:"stateDir"`
		PublicURL string `json:"publicUrl"`
		Phase     string `json:"phase"`
	}
	data, err := os.ReadFile(*configPath)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return err
	}
	if config.Phase != "active" || config.DataDir == "" || config.StateDir == "" || config.PublicURL == "" {
		return errors.New("fixture requires an active local controller configuration")
	}
	if _, err = os.Stat(filepath.Join(config.DataDir, "hqip.db")); err != nil {
		return err
	}
	db, err := store.Open(config.DataDir, config.PublicURL)
	if err != nil {
		return err
	}
	defer db.Close()
	file := filepath.Join(config.StateDir, "fixture-b", "node-b.json")
	if args[0] == "prepare" {
		if *nodeA == "" {
			return errors.New("--node-a is required")
		}
		if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("fixture output already exists or cannot be created: %w", err)
		}
		defer out.Close()
		f, err := prepareFixture(db, *nodeA, func(f fixture) error { return json.NewEncoder(out).Encode(f) })
		if err != nil {
			_ = out.Close()
			_ = os.Remove(file)
			return err
		}
		fmt.Printf("Fixture registered without installation: nodeA=%s nodeB=%s device=%s\nPrivate configuration: %s\n", f.NodeA, f.NodeID, f.DeviceID, file)
		return nil
	}
	data, err = os.ReadFile(file)
	if err != nil {
		return err
	}
	var f fixture
	if err = json.Unmarshal(data, &f); err != nil {
		return err
	}
	if err = cleanupFixture(db, f); err != nil {
		return err
	}
	fmt.Println("Fixture disabled; node, device and usage history retained. Also run the generated remote cleanup script to stop B services.")
	return nil
}

func prepareFixture(db *store.Store, nodeA string, write func(fixture) error) (fixture, error) {
	var f fixture
	a, err := db.Node(nodeA)
	if err != nil {
		return f, err
	}
	if a.Status != "ready" || a.Port == 8443 {
		return f, errors.New("node A must be ready on a port other than 8443")
	}
	var bytes [32]byte
	if _, err = rand.Read(bytes[:]); err != nil {
		return f, err
	}
	f = fixture{Marker: "hqip-acceptance-b-v1", NodeA: a.ID, NodeID: store.NewID(), DeviceID: store.NewID(), Token: secure.Token(), ControlURL: db.BaseURL, StatsSecret: hex.EncodeToString(bytes[:]), PublicIP: a.PublicIP, Domain: a.Domain, Port: 8443, ProbePassword: secure.Token()}
	b := model.Node{ID: f.NodeID, Name: "[临时验收] 节点 B · UDP 8443", SSHHost: a.SSHHost, SSHPort: a.SSHPort, SSHUser: a.SSHUser, PublicIP: a.PublicIP, Domain: a.Domain, Port: 8443, HostKey: a.HostKey, CreatedAt: db.Now().UTC(), Status: "awaiting_external", Message: "隔离验收节点：尚未执行远端安装或外部验证"}
	raw, err := json.Marshal(b)
	if err != nil {
		return f, err
	}
	encAgent, err := db.Vault.Encrypt(f.Token)
	if err != nil {
		return f, err
	}
	subToken := secure.Token()
	encSub, err := db.Vault.Encrypt(subToken)
	if err != nil {
		return f, err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT COUNT(*) FROM metadata WHERE key LIKE 'fixture.acceptance-b.%' AND value!='disabled'").Scan(&count); err != nil {
		return f, err
	}
	if count != 0 {
		return f, errors.New("an active B fixture already exists")
	}
	if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE json_extract(config,'$.publicIp')=? AND json_extract(config,'$.port')=8443 AND json_extract(config,'$.status')!='fixture_disabled'", a.PublicIP).Scan(&count); err != nil {
		return f, err
	}
	if count != 0 {
		return f, errors.New("a node already uses the fixture endpoint")
	}
	if _, err = tx.Exec("INSERT INTO nodes(id,config,agent_hash,agent_secret,desired_revision) VALUES(?,?,?,?,2)", b.ID, string(raw), secure.Hash(f.Token), encAgent); err != nil {
		return f, err
	}
	if _, err = tx.Exec("INSERT INTO devices(id,name,created_at,token_hash,token_secret) VALUES(?,?,?,?,?)", f.DeviceID, "[临时验收] Windows 双节点切换", db.Now().Unix(), secure.Hash(subToken), encSub); err != nil {
		return f, err
	}
	if _, err = tx.Exec("INSERT INTO device_config(device_id,default_node_id) VALUES(?,?)", f.DeviceID, a.ID); err != nil {
		return f, err
	}
	for _, row := range []struct{ node, device, password string }{{b.ID, store.ProbeID(b.ID), f.ProbePassword}, {a.ID, f.DeviceID, secure.Token()}, {b.ID, f.DeviceID, secure.Token()}} {
		enc, err := db.Vault.Encrypt(row.password)
		if err != nil {
			return f, err
		}
		if _, err = tx.Exec("INSERT INTO credentials(node_id,device_id,password) VALUES(?,?,?)", row.node, row.device, enc); err != nil {
			return f, err
		}
	}
	if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", a.ID); err != nil {
		return f, err
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err = tx.Exec("INSERT INTO assignments(device_id,node_id,required_revision) SELECT ?,id,desired_revision FROM nodes WHERE id=?", f.DeviceID, id); err != nil {
			return f, err
		}
	}
	if _, err = tx.Exec("INSERT INTO metadata(key,value) VALUES(?,?)", "fixture.acceptance-b."+b.ID, f.DeviceID); err != nil {
		return f, err
	}
	if err = write(f); err != nil {
		return f, err
	}
	if err = tx.Commit(); err != nil {
		return f, err
	}
	db.Audit("fixture.prepare", b.ID)
	return f, nil
}

func cleanupFixture(db *store.Store, f fixture) error {
	if f.Marker != "hqip-acceptance-b-v1" || f.NodeID == "" || f.DeviceID == "" || f.NodeA == f.NodeID {
		return errors.New("invalid fixture identity")
	}
	var marker string
	if err := db.DB.QueryRow("SELECT value FROM metadata WHERE key=?", "fixture.acceptance-b."+f.NodeID).Scan(&marker); err != nil {
		return err
	}
	if marker == "disabled" {
		return nil
	}
	if marker != f.DeviceID {
		return errors.New("fixture marker mismatch")
	}
	var other int
	if err := db.DB.QueryRow("SELECT COUNT(*) FROM assignments WHERE node_id=? AND device_id!=?", f.NodeID, f.DeviceID).Scan(&other); err != nil {
		return err
	}
	if other != 0 {
		return errors.New("remove B assignments from non-fixture devices before cleanup")
	}
	d, err := db.Device(f.DeviceID)
	if err != nil {
		return err
	}
	d.Enabled = false
	if _, err = db.UpdateDevice(d.ID, nil, &d.Enabled, false); err != nil {
		return err
	}
	a, err := db.Assignment(d.ID)
	if err != nil {
		return err
	}
	if _, err = db.Assign(d.ID, nil, "", a.Revision); err != nil {
		return err
	}
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE nodes SET agent_hash=?,config=json_set(config,'$.status','fixture_disabled','$.message','临时验收已结束，历史记录保留'),core_healthy=0 WHERE id=?", secure.Hash(secure.Token()), f.NodeID); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE metadata SET value='disabled' WHERE key=?", "fixture.acceptance-b."+f.NodeID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	db.Audit("fixture.cleanup", f.NodeID)
	return nil
}
