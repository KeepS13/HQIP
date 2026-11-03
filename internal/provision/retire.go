package provision

import (
	"context"
	"errors"
	"fmt"
	"github.com/KeepS13/HQIP/internal/model"
	"strings"
	"time"
)

// Retire is the offline recovery path. It never treats an old single-instance
// HQIP installation or an unrelated Hysteria as belonging to this node.
func (r *Runner) Retire(ctx context.Context, in Input, log LogFunc, pin PinFunc) (res Result, err error) {
	if err = Validate(in.Node); err != nil {
		return
	}
	if log == nil {
		log = func(string, string) {}
	}
	rm, e := r.connect(ctx, in, pin)
	if e != nil {
		return res, e
	}
	defer rm.client.Close()
	unlock, e := rm.lock(ctx)
	if e != nil {
		return res, e
	}
	defer unlock()
	inv, e := r.inventory(ctx, rm, in.Node)
	if e != nil {
		return res, e
	}
	p := pathsFor(in.Node.ID)
	res.MachineID = inv.MachineID
	if inv.Owner == nil {
		if err := confirmMissingInstance(in.Node, inv); err != nil {
			return res, err
		}
		res.Message = "未发现属于此节点的受管实例，其他部署保持不变"
		return res, nil
	}
	if !r.assess(in.Node, inv).ExistingManaged {
		return res, errors.New("节点归属不匹配，拒绝停止或删除其他实例")
	}
	res.Managed = true
	res.InstallationVersion = installationVersion
	res.BackupPath = "/var/backups/hqip-nodes/" + in.Node.ID + "-retired-" + time.Now().UTC().Format("20060102T150405") + "-" + secret()[:12]
	log("retire", "验证本节点归属，停止并归档本节点服务")
	// Stop first so the SQLite WAL and local ledger in the archive form a
	// consistent cold backup. A failed archive leaves stopped data in place.
	for _, unit := range []string{p.CoreUnit, p.AgentUnit} {
		loaded, loadErr := rm.run(ctx, "systemctl show --property=LoadState --value "+shellQuote(unit), nil)
		if loadErr != nil {
			return res, errors.New("无法确认待删除服务状态")
		}
		if strings.TrimSpace(loaded) == "not-found" {
			continue
		}
		if strings.TrimSpace(loaded) != "loaded" {
			return res, errors.New("节点服务状态异常，拒绝清理")
		}
		command := "systemctl stop " + shellQuote(unit) + " && systemctl disable " + shellQuote(unit) + " >/dev/null"
		if _, err = rm.run(ctx, command, nil); err != nil {
			return res, errors.New("停止节点独立服务失败")
		}
		out, e := rm.run(ctx, "systemctl show --property=ActiveState --value "+shellQuote(unit), nil)
		if e != nil || (strings.TrimSpace(out) != "inactive" && strings.TrimSpace(out) != "failed") {
			return res, errors.New("尚未确认节点服务已经停止")
		}
	}
	payload := map[string]any{"action": "backup", "nodeId": in.Node.ID, "backup": res.BackupPath, "candidates": []TakeoverCandidate{}}
	if _, err = rm.run(ctx, pythonCommand(backupScript, payload), nil); err != nil {
		return res, errors.New("节点已停止，但冷备份失败；未清理文件")
	}
	if err = cleanFirewall(ctx, rm, in.Node.ID, nil); err != nil {
		return res, errors.New("节点已停止并备份，但清理自有防火墙规则失败")
	}
	cleanup := `import base64,json,pathlib,os,shutil
a=json.loads(base64.b64decode(__import__('sys').argv[1])); p=pathlib.Path(a['config']); backup=pathlib.Path(a['backup'])
owner=json.loads((p/'owner.json').read_text())
if owner.get('nodeId')!=a['nodeId'] or owner.get('controllerId')!=a['controllerId']: raise SystemExit('owner-changed')
for name in ('agent.json','hysteria.yaml'):
 f=p/name
 if f.exists(): f.rename(backup/name)
for name in ('certificates','acme'):
 f=pathlib.Path(a['state'])/name
 if os.path.lexists(f): f.rename(backup/name)
for unit in a['units']:
 pathlib.Path('/etc/systemd/system',unit).unlink(missing_ok=True)
`
	_, err = rm.run(ctx, pythonCommand(cleanup, map[string]any{"nodeId": in.Node.ID, "controllerId": r.controllerIdentity(), "config": p.Config, "state": p.State, "backup": res.BackupPath, "units": []string{p.AgentUnit, p.CoreUnit}})+" && systemctl daemon-reload", nil)
	if err != nil {
		return res, errors.New("节点已停止并备份，但文件清理尚未完成，可安全重试")
	}
	res.Message = fmt.Sprintf("本节点服务已停止并禁用；备份：%s", res.BackupPath)
	return res, nil
}

func confirmMissingInstance(n model.Node, inv hostInventory) error {
	if n.Managed && n.InstallationVersion < installationVersion || inv.LegacyOwner == n.ID {
		return errors.New("仍有旧版节点归属，不能确认已停止；请先预检并接管升级，或完成旧实例的人工清理")
	}
	p := pathsFor(n.ID)
	for _, unit := range inv.Units {
		if unit.Unit == p.CoreUnit || unit.Unit == p.AgentUnit {
			return errors.New("节点归属记录缺失但服务存在，拒绝清理")
		}
	}
	return nil
}
