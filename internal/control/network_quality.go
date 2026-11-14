package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
)

type networkQualityRunner interface {
	NetworkQuality(context.Context, provision.Input, provision.PinFunc) (json.RawMessage, error)
}

func (s *Server) registerNetworkQualityRoutes(m *http.ServeMux) {
	m.Handle("GET /api/v1/nodes/{id}/network-quality", s.auth(http.HandlerFunc(s.getNetworkQuality), false))
	m.Handle("GET /api/v1/nodes/{id}/network-quality/image", s.auth(http.HandlerFunc(s.getNetworkQualityImage), false))
	m.Handle("POST /api/v1/nodes/{id}/network-quality", s.auth(http.HandlerFunc(s.queueNetworkQuality), false))
}
func (s *Server) getNetworkQualityImage(w http.ResponseWriter, r *http.Request) {
	version := r.URL.Query().Get("version")
	if _, err := hex.DecodeString(version); err != nil || len(version) != 64 {
		fail(w, http.StatusBadRequest, "请选择当前报告图片")
		return
	}
	image, err := s.Store.NetworkQualityImage(r.PathValue("id"), version)
	if err != nil {
		managementError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", `inline; filename="hqip-ip-quality.svg"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(image)
}
func (s *Server) getNetworkQuality(w http.ResponseWriter, r *http.Request) {
	v, err := s.Store.NetworkQuality(r.PathValue("id"))
	result(w, v, err)
}
func (s *Server) queueNetworkQuality(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.runner.(networkQualityRunner); !ok {
		fail(w, 503, "当前部署组件不支持网络质量检测")
		return
	}
	var body struct {
		Password         string `json:"password"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Store.QueueNetworkQuality(r.PathValue("id"), body.Password, body.ExpectedRevision)
	if errors.Is(err, store.ErrQualityCooldown) {
		w.Header().Set("Retry-After", "600")
		fail(w, 429, err.Error())
		return
	}
	if err != nil {
		managementError(w, err)
		return
	}
	s.signal()
	reply(w, http.StatusAccepted, v)
}

func (s *Server) runQualityJob(parent context.Context, job store.Job) {
	// Never turn a failed optional diagnostic into a node deployment failure.
	defer func() { s.Store.ClearSSHPassword(job.NodeID); s.finishJob(parent, job, true) }()
	n, err := s.Store.Node(job.NodeID)
	if err != nil {
		_ = s.Store.FinishNetworkQuality(job.NodeID, job.ID, "cancelled", "节点已不可用", nil)
		return
	}
	password, _, _, err := s.Store.NodeSecrets(n.ID)
	if err != nil || password == "" {
		_ = s.Store.FinishNetworkQuality(n.ID, job.ID, "failed", "SSH 凭据已过期，请重新输入密码检测", nil)
		return
	}
	checker, ok := s.runner.(networkQualityRunner)
	if !ok {
		_ = s.Store.FinishNetworkQuality(n.ID, job.ID, "failed", "当前部署组件不支持网络质量检测", nil)
		return
	}
	s.checkNetworkQuality(parent, job, provision.Input{Node: n, Password: password}, checker)
}

func (s *Server) checkNetworkQuality(parent context.Context, job store.Job, input provision.Input, checker networkQualityRunner) {
	if err := s.Store.StartNetworkQuality(input.Node.ID, job.ID, input.Node.PublicIP); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 6*time.Minute)
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				active, err := s.Store.JobActive(job.ID)
				if err != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	_ = s.Store.Event(input.Node.ID, "quality", "开始检测节点指定 IP 的网络质量，结果不影响代理就绪状态")
	report, err := checker.NetworkQuality(ctx, input, func(fingerprint string) error { return s.Store.Pin(input.Node.ID, fingerprint) })
	status, message := "complete", ""
	if err != nil {
		// Never store arbitrary command output, request URLs, or credentials.
		status, message = "failed", "网络质量检测未完成，请检查 SSH、依赖和外部检测服务后重试"
		var publicError *provision.QualityError
		if errors.As(err, &publicError) {
			message = publicError.Message
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "网络质量检测超过 6 分钟，已停止"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			status, message = "cancelled", "检测已取消，保留最近一次成功报告"
		}
	}
	if active, e := s.Store.JobActive(job.ID); e != nil || !active {
		status, message = "cancelled", "节点任务已取消，保留最近一次成功报告"
	}
	if e := s.Store.FinishNetworkQuality(input.Node.ID, job.ID, status, message, report); e != nil {
		_ = s.Store.FinishNetworkQuality(input.Node.ID, job.ID, "failed", "检测返回了无效报告，请重试", nil)
	}
	label := "网络质量报告已保存"
	if status != "complete" {
		label = message
	}
	_ = s.Store.Event(input.Node.ID, "quality", label)
}
