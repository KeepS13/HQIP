package control

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
)

type serverOperator interface {
	ChangeServerPassword(context.Context, model.Server, string, string, provision.PinFunc) error
	AgentRelease() (provision.AgentRelease, error)
	UpgradeAgent(context.Context, provision.Input, string, string, provision.LogFunc, provision.PinFunc) error
}

func (s *Server) savedNodePassword(id string) (string, error) {
	n, err := s.Store.Node(id)
	if err != nil {
		return "", err
	}
	v, err := s.Store.Server(n.ServerID)
	if err != nil {
		return "", err
	}
	return s.Store.ServerPassword(v)
}

func (s *Server) registerServerOperations(m *http.ServeMux) {
	for path, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/servers/{id}/credential":        s.serverCredential,
		"PUT /api/v1/servers/{id}/credential":        s.serverCredential,
		"DELETE /api/v1/servers/{id}/credential":     s.serverCredential,
		"POST /api/v1/servers/{id}/credential/check": s.checkServerCredential,
		"POST /api/v1/servers/{id}/password":         s.changeServerPassword,
		"GET /api/v1/server-operations":              s.serverOperations,
		"POST /api/v1/server-operations/{id}/retry":  s.retryServerOperation,
		"GET /api/v1/agent-release":                  s.agentRelease,
		"POST /api/v1/agent-upgrades":                s.queueAgentUpgrades,
	} {
		m.Handle(path, s.auth(handler, false))
	}
}
func (s *Server) serverCredential(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == "GET" {
		v, e := s.Store.ServerCredential(id)
		result(w, v, e)
		return
	}
	var body struct {
		Password           string `json:"password"`
		ExpectedRevision   int64  `json:"expectedRevision"`
		CredentialRevision int64  `json:"credentialRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if r.Method == "DELETE" {
		if e := s.Store.DeleteServerCredential(id, body.CredentialRevision); e != nil {
			managementError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
		return
	}
	if body.Password == "" || len(body.Password) > 1024 {
		fail(w, 400, "请填写 SSH 密码")
		return
	}
	v, e := s.Store.Server(id)
	if e != nil {
		managementError(w, e)
		return
	}
	if v.Revision != body.ExpectedRevision {
		managementError(w, store.ErrConflict)
		return
	}
	checker, ok := s.runner.(serverInspector)
	if !ok {
		fail(w, 503, "当前部署组件不支持凭据检查")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 24*time.Second)
	defer cancel()
	_, e = checker.CheckServer(ctx, v, body.Password, pinnedServer(v))
	if e != nil {
		fail(w, 400, credentialMessage(e))
		return
	}
	if e = s.Store.SaveServerCredential(id, body.Password, body.ExpectedRevision, body.CredentialRevision); e != nil {
		managementError(w, e)
		return
	}
	status, e := s.Store.ServerCredential(id)
	result(w, status, e)
}
func pinnedServer(v model.Server) provision.PinFunc {
	return func(pin string) error {
		if v.HostKey == "" || v.HostKey != pin {
			return provision.ErrSSHIdentity
		}
		return nil
	}
}
func credentialMessage(err error) string {
	if errors.Is(err, provision.ErrSSHAuthentication) {
		return "SSH 密码认证失败，请更新凭据"
	}
	if errors.Is(err, provision.ErrSSHIdentity) {
		return "服务器 SSH 主机指纹发生变化，已停止操作"
	}
	return "SSH 连接或主机检查失败，请检查网络、端口和服务器状态"
}
func (s *Server) checkServerCredential(w http.ResponseWriter, r *http.Request) {
	j, e := s.Store.QueueServerOperation(r.PathValue("id"), "", "check", "", "", 0)
	if e != nil {
		managementError(w, e)
		return
	}
	s.signal()
	reply(w, 202, j)
}
func (s *Server) changeServerPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.runner.(serverOperator); !ok {
		fail(w, 503, "当前部署组件不支持改密")
		return
	}
	var body struct {
		NewPassword      string `json:"newPassword"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.NewPassword) < 12 {
		fail(w, 400, "新密码至少需要 12 个字节")
		return
	}
	j, e := s.Store.QueueServerOperation(r.PathValue("id"), "", "password", body.NewPassword, "", body.ExpectedRevision)
	if e != nil {
		managementError(w, e)
		return
	}
	s.signal()
	reply(w, 202, j)
}
func (s *Server) serverOperations(w http.ResponseWriter, r *http.Request) {
	v, e := s.Store.ServerOperations(r.URL.Query().Get("serverId"))
	result(w, v, e)
}
func (s *Server) retryServerOperation(w http.ResponseWriter, r *http.Request) {
	if e := s.Store.RetryServerOperation(r.PathValue("id")); e != nil {
		managementError(w, e)
		return
	}
	s.signal()
	reply(w, 202, map[string]bool{"queued": true})
}
func (s *Server) agentRelease(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.runner.(serverOperator)
	if !ok {
		fail(w, 503, "当前部署组件不支持 Agent 升级")
		return
	}
	v, e := runner.AgentRelease()
	if e != nil {
		fail(w, 503, "Agent 发布产物暂不可用")
		return
	}
	reply(w, 200, v)
}
func (s *Server) queueAgentUpgrades(w http.ResponseWriter, r *http.Request) {
	runner, ok := s.runner.(serverOperator)
	if !ok {
		fail(w, 503, "当前部署组件不支持 Agent 升级")
		return
	}
	var body struct {
		NodeIDs   []string `json:"nodeIds"`
		SourceSHA string   `json:"sourceSHA"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.NodeIDs) == 0 || len(body.NodeIDs) > 100 {
		fail(w, 400, "每批请选择 1～100 个节点")
		return
	}
	release, e := runner.AgentRelease()
	if e != nil {
		fail(w, 503, "Agent 发布产物暂不可用")
		return
	}
	if body.SourceSHA != release.SourceSHA {
		fail(w, 409, "发布版本已变化，请刷新后重试")
		return
	}
	type entry struct {
		NodeID string                 `json:"nodeId"`
		Job    *store.ServerOperation `json:"job,omitempty"`
		Error  string                 `json:"error,omitempty"`
	}
	out := []entry{}
	seen := map[string]bool{}
	for _, id := range body.NodeIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		n, e := s.Store.Node(id)
		if e != nil {
			out = append(out, entry{NodeID: id, Error: "节点不存在"})
			continue
		}
		j, e := s.Store.QueueServerOperation(n.ServerID, id, "agent-upgrade", "", release.SourceSHA, 0)
		if e != nil {
			out = append(out, entry{NodeID: id, Error: e.Error()})
		} else {
			out = append(out, entry{NodeID: id, Job: &j})
		}
	}
	s.signal()
	reply(w, 202, out)
}
func (s *Server) runServerOperation(parent context.Context, j store.ServerOperation) {
	v, password, next, err := s.Store.OperationCredentials(j)
	if err != nil {
		retryTaskState(parent, "保存服务器任务结束状态", func() error {
			return s.Store.FinishServerOperation(j, "failed", "服务器或凭据已变化，请重新发起任务", "")
		})
		return
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	state, message, status := "succeeded", "操作完成", ""
	switch j.Kind {
	case "check":
		checker, ok := s.runner.(serverInspector)
		if !ok {
			err = errors.New("不支持凭据检查")
		} else {
			checkCtx, done := context.WithTimeout(ctx, 40*time.Second)
			_, err = checker.CheckServer(checkCtx, v, password, pinnedServer(v))
			done()
		}
		status = "healthy"
		message = "SSH 凭据验证通过"
		if err != nil {
			state = "failed"
			status = "unreachable"
			message = credentialMessage(err)
			if errors.Is(err, provision.ErrSSHAuthentication) {
				status = "invalid"
			}
			if errors.Is(err, provision.ErrSSHIdentity) {
				status = "identity-changed"
			}
		}
	case "password":
		runner, ok := s.runner.(serverOperator)
		if !ok {
			err = errors.New("不支持改密")
		} else {
			err = runner.ChangeServerPassword(ctx, v, password, next, pinnedServer(v))
		}
		if err != nil {
			state = "uncertain"
			message = "改密结果尚未确认，恢复凭据已保留，请重试核对"
		} else {
			message = "服务器密码已修改，新密码登录验证通过，托管凭据已更新"
		}
	case "agent-upgrade":
		runner, ok := s.runner.(serverOperator)
		if !ok {
			err = errors.New("不支持升级")
		} else {
			var n model.Node
			n, err = s.Store.Node(j.NodeID)
			if err == nil && (n.Lifecycle != "active" || n.ServerID != v.ID) {
				err = store.ErrConflict
			}
			if err == nil {
				err = runner.UpgradeAgent(ctx, provision.Input{Node: n, Password: password}, j.Target, j.ID, func(stage, message string) { _ = s.Store.Event(n.ID, stage, message) }, pinnedServer(v))
			}
		}
		message = "Agent 升级成功，转发核心未重启"
		if err != nil {
			state = "failed"
			message = "Agent 升级失败，请检查节点事件或 SSH 凭据后重试"
			if errors.Is(err, provision.ErrSSHAuthentication) || errors.Is(err, provision.ErrSSHIdentity) {
				message = credentialMessage(err)
			}
		}
	}
	if parent.Err() != nil {
		return
	} // Recovery checks remote state before repeating a password change.
	retryTaskState(parent, "保存服务器任务结束状态", func() error {
		return s.Store.FinishServerOperation(j, state, message, status)
	})
}
