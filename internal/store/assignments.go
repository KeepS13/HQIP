package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
)

var ErrConflict = errors.New("配置已被其他操作更新，请刷新后重试")
var ErrUnauthorized = errors.New("账号、密码或会话无效")

func (s *Store) Assignment(id string) (model.Assignment, error) {
	a := model.Assignment{NodeIDs: []string{}}
	err := s.DB.QueryRow("SELECT default_node_id,revision FROM device_config WHERE device_id=?", id).Scan(&a.DefaultNodeID, &a.Revision)
	if err != nil {
		return a, notFound(err)
	}
	rows, err := s.DB.Query("SELECT node_id FROM assignments WHERE device_id=? ORDER BY node_id", id)
	if err != nil {
		return a, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return a, err
		}
		a.NodeIDs = append(a.NodeIDs, n)
	}
	return a, rows.Err()
}

func (s *Store) Assign(id string, nodeIDs []string, defaultID string, expected int64) (model.Assignment, error) {
	if len(nodeIDs) > 1000 {
		return model.Assignment{}, errors.New("每个设备最多分配 1000 个节点")
	}
	set := map[string]bool{}
	for _, n := range nodeIDs {
		if n == "" || set[n] {
			return model.Assignment{}, errors.New("节点列表含空值或重复节点")
		}
		set[n] = true
	}
	if (len(set) == 0 && defaultID != "") || (len(set) > 0 && !set[defaultID]) {
		return model.Assignment{}, errors.New("默认节点必须属于分配的节点集合")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return model.Assignment{}, err
	}
	defer tx.Rollback()
	var revision int64
	var oldDefault string
	if err = tx.QueryRow("SELECT revision,default_node_id FROM device_config WHERE device_id=?", id).Scan(&revision, &oldDefault); err != nil {
		return model.Assignment{}, notFound(err)
	}
	if revision != expected {
		return model.Assignment{}, ErrConflict
	}
	rows, err := tx.Query("SELECT node_id FROM assignments WHERE device_id=?", id)
	if err != nil {
		return model.Assignment{}, err
	}
	old := map[string]bool{}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return model.Assignment{}, err
		}
		old[n] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return model.Assignment{}, err
	}
	changed := oldDefault != defaultID || len(old) != len(set)
	for n := range set {
		if !old[n] {
			changed = true
		}
		var exists int
		if err = tx.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", n).Scan(&exists); err != nil {
			return model.Assignment{}, err
		}
		if exists != 1 {
			return model.Assignment{}, errors.New("分配列表包含不存在的节点")
		}
	}
	if !changed {
		tx.Rollback()
		return s.Assignment(id)
	}
	revision++
	if _, err = tx.Exec("UPDATE device_config SET revision=?,default_node_id=? WHERE device_id=?", revision, defaultID, id); err != nil {
		return model.Assignment{}, err
	}
	for n := range set {
		if old[n] {
			continue
		}
		pass, e := s.Vault.Encrypt(secure.Token())
		if e != nil {
			return model.Assignment{}, e
		}
		if _, err = tx.Exec("INSERT INTO credentials(node_id,device_id,password) VALUES(?,?,?) ON CONFLICT(node_id,device_id) DO UPDATE SET password=excluded.password", n, id, pass); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("UPDATE removals SET status='cancelled' WHERE device_id=? AND node_id=? AND status NOT IN ('revoked','cancelled')", id, n); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", n); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("INSERT INTO assignments(device_id,node_id,required_revision) SELECT ?,id,desired_revision FROM nodes WHERE id=?", id, n); err != nil {
			return model.Assignment{}, err
		}
	}
	for n := range old {
		if set[n] {
			continue
		}
		removalID := NewID()
		if _, err = tx.Exec("DELETE FROM assignments WHERE device_id=? AND node_id=?", id, n); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("INSERT INTO removals(id,device_id,node_id,revision,created_at) VALUES(?,?,?,?,?)", removalID, id, n, revision, s.Now().Unix()); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("INSERT INTO removal_participants(removal_id,session_id) SELECT ?,id FROM client_sessions WHERE device_id=? AND revoked=0 AND refresh_expires>? AND last_seen>?", removalID, id, s.Now().Unix(), s.Now().Add(-30*time.Second).Unix()); err != nil {
			return model.Assignment{}, err
		}
		if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", n); err != nil {
			return model.Assignment{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return model.Assignment{}, err
	}
	s.Audit("device.assignment", id)
	return s.Assignment(id)
}

func (s *Store) AssignedNodes(id string, readyOnly bool) ([]model.Node, error) {
	a, err := s.Assignment(id)
	if err != nil {
		return nil, err
	}
	result := []model.Node{}
	for _, nid := range a.NodeIDs {
		n, e := s.Node(nid)
		if e != nil {
			return nil, e
		}
		if !readyOnly || n.Status == "ready" {
			result = append(result, n)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].ID == a.DefaultNodeID && result[j].ID != a.DefaultNodeID })
	return result, nil
}
func (s *Store) Removals(deviceID string) ([]model.Removal, error) {
	rows, err := s.DB.Query("SELECT id,node_id,device_id,revision,status,deadline,created_at,immediate FROM removals WHERE device_id=? AND status!='cancelled' ORDER BY created_at DESC,id", deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.Removal{}
	for rows.Next() {
		var r model.Removal
		var deadline, created int64
		if err = rows.Scan(&r.ID, &r.NodeID, &r.DeviceID, &r.Revision, &r.Status, &deadline, &created, &r.Immediate); err != nil {
			return nil, err
		}
		if deadline > 0 {
			t := time.Unix(deadline, 0).UTC()
			r.Deadline = &t
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *Store) recordRemovals(tx *sql.Tx, nodeID string, reports []model.AgentRemovalStatus) error {
	if len(reports) > 4000 {
		return errors.New("撤销上报记录过多")
	}
	for _, r := range reports {
		if r.Status != "scheduled" && r.Status != "revoked" {
			return errors.New("撤销状态无效")
		}
		deadline := r.Deadline.Unix()
		if r.Deadline.IsZero() {
			deadline = 0
		}
		if _, err := tx.Exec("UPDATE removals SET status=?,deadline=CASE WHEN deadline=0 THEN ? ELSE MIN(deadline,?) END WHERE id=? AND node_id=? AND device_id=? AND status NOT IN ('revoked','cancelled')", r.Status, deadline, deadline, r.ID, nodeID, r.DeviceID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) settleRemovals(tx *sql.Tx, deviceID string) error {
	rows, err := tx.Query("SELECT id,node_id,revision FROM removals WHERE device_id=? AND status NOT IN ('revoked','cancelled') AND immediate=0", deviceID)
	if err != nil {
		return err
	}
	type removal struct {
		id, node string
		rev      int64
	}
	pending := []removal{}
	for rows.Next() {
		var r removal
		if err = rows.Scan(&r.id, &r.node, &r.rev); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range pending {
		var participants, waiting int
		if err = tx.QueryRow("SELECT COUNT(*),COALESCE(SUM(CASE WHEN cs.revoked=0 AND (MAX(COALESCE(json_extract(cs.status,'$.processedConfigRevision'),0),COALESCE(json_extract(cs.status,'$.appliedConfigRevision'),0))<? OR COALESCE(json_extract(cs.status,'$.runState'),'') IN ('','connecting')) THEN 1 ELSE 0 END),0) FROM removal_participants rp JOIN client_sessions cs ON cs.id=rp.session_id WHERE rp.removal_id=?", r.rev, r.id).Scan(&participants, &waiting); err != nil {
			return err
		}
		if participants > 0 && waiting == 0 {
			if _, err = tx.Exec("UPDATE removals SET immediate=1 WHERE id=?", r.id); err != nil {
				return err
			}
			if _, err = tx.Exec("UPDATE nodes SET desired_revision=desired_revision+1 WHERE id=?", r.node); err != nil {
				return err
			}
		}
	}
	return nil
}
