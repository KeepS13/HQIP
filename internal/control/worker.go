package control

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/provision"
	"github.com/KeepS13/HQIP/internal/store"
)

func (s *Server) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Server) RunWorker(ctx context.Context) error {
	if s.initErr != nil {
		return s.initErr
	}
	if err := s.Store.RecoverNetworkQuality(); err != nil {
		return err
	}
	if err := s.Store.RecoverJobs(); err != nil {
		return err
	}
	_ = s.Store.Maintenance()
	maintenanceCtx, stopMaintenance := context.WithCancel(ctx)
	defer stopMaintenance()
	s.cloudflareMaintenance(maintenanceCtx)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-maintenanceCtx.Done():
				return
			case <-ticker.C:
				_ = s.Store.Maintenance()
				s.cloudflareMaintenance(maintenanceCtx)
			}
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		job, err := s.Store.NextJob()
		if err == nil {
			s.runJob(ctx, job)
			continue
		}
		if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
		case <-ticker.C:
		}
	}
}
func (s *Server) runJob(parent context.Context, job store.Job) {
	if job.Kind == "quality" {
		s.runQualityJob(parent, job)
		return
	}
	node, err := s.Store.Node(job.NodeID)
	if err != nil {
		s.Store.FinishJob(job, false)
		return
	}
	password, token, probe, err := s.Store.NodeSecrets(node.ID)
	if err != nil {
		_ = s.Store.SetStatus(node.ID, "error", "读取节点凭据失败，请检查主密钥", node.SharedHost)
		s.Store.FinishJob(job, false)
		return
	}
	if (job.Kind == "install" || job.Kind == "retire") && password == "" {
		_ = s.Store.SetStatus(node.ID, "error", "SSH 密码已过期，请重新输入密码并重试", node.SharedHost)
		_ = s.Store.Event(node.ID, "error", "SSH 密码已过期，请重新输入密码并重试")
		s.Store.FinishJob(job, false)
		return
	}
	redact := func(message string) string {
		for _, secret := range []string{password, token, probe} {
			if secret != "" {
				message = strings.ReplaceAll(message, secret, "[已隐藏]")
			}
		}
		return message
	}
	log := func(stage, message string) { _ = s.Store.Event(node.ID, stage, redact(message)) }
	ctx, cancel := context.WithTimeout(parent, 30*time.Minute)
	defer cancel()
	// Deletion cancels an in-flight installer, but its durable 'cancelling'
	// job retains the host lock until ownership and rollback have settled.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				active, checkErr := s.Store.JobActive(job.ID)
				if checkErr != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	input := provision.Input{Node: node, Password: password, AgentToken: token, ProbePassword: probe, Takeover: job.Takeover, PreflightFingerprint: job.PreflightFingerprint}
	if job.CertificateMode != "" {
		input.Node.CertificateMode, input.Node.ZoneID = job.CertificateMode, job.ZoneID
	}
	var output provision.Result
	if job.Kind == "install" {
		_ = s.Store.SetStatus(node.ID, "installing", "正在通过 SSH 安装节点", node.SharedHost)
		input.Node, input.Certificate, err = s.prepareNodeCloudflare(ctx, input.Node)
		if err == nil {
			output, err = s.runner.Install(ctx, input, log, func(fingerprint string) error { return s.Store.Pin(node.ID, fingerprint) })
		}
		if output.Managed {
			_ = s.Store.MarkInstalled(node.ID)
		}
	} else if job.Kind == "retire" {
		retirer, ok := s.runner.(interface {
			Retire(context.Context, provision.Input, provision.LogFunc, provision.PinFunc) (provision.Result, error)
		})
		if !ok {
			err = errors.New("当前部署组件不支持 SSH 清理")
		} else {
			output, err = retirer.Retire(ctx, input, log, func(fingerprint string) error { return s.Store.Pin(node.ID, fingerprint) })
		}
		if err == nil {
			err = s.Store.CompleteNodeRetirement(node.ID)
		}
	} else {
		_ = s.Store.SetStatus(node.ID, "verifying", "正在验证代理连接", node.SharedHost)
		output, err = s.runner.Verify(ctx, input, log)
	}
	if parent.Err() != nil {
		return
	} // The durable running job is requeued after restart.
	active, checkErr := s.Store.JobActive(job.ID)
	if checkErr != nil || !active {
		log("cancelled", "部署任务已取消，保留需要确认清理的安装记录")
		s.Store.FinishJob(job, false)
		return
	}
	if job.Kind == "retire" {
		if err != nil {
			log("error", "节点清理尚未完成："+redact(err.Error()))
		} else {
			log("deleted", "已确认本节点服务停止并清理")
			s.cloudflareMaintenance(parent)
		}
		s.Store.FinishJob(job, err == nil)
		return
	}
	if err == nil && job.Kind == "install" && job.CertificateMode != "" {
		err = s.Store.CommitNodeCertificateMode(node.ID, job.ID, job.CertificateMode, job.ZoneID)
	}
	if err != nil {
		message := "部署或验证失败：" + redact(err.Error())
		_ = s.Store.SetStatus(node.ID, "error", message, output.SharedHost || node.SharedHost)
		log("error", message)
		s.Store.FinishJob(job, false)
		return
	}
	if job.Kind == "install" {
		s.Store.ClearSSHPassword(node.ID)
		if output.InstallationVersion > 0 {
			_ = s.Store.SetInstallationVersion(node.ID, output.InstallationVersion)
		}
		if output.MachineID != "" && node.ServerID != "" {
			current, readErr := s.Store.Node(node.ID)
			if readErr == nil {
				_ = s.Store.PinServer(node.ServerID, current.HostKey, output.MachineID)
			}
		}
	}
	_ = s.Store.SetTopology(node.ID, output.ControlOnNode, output.ACMEViaCaddy)
	status, message := "error", "代理验证未通过，请检查端口及网络"
	if output.ControlOnNode {
		status = "awaiting_external"
		message = "本机代理验证通过，等待从用户电脑验证 UDP 和出口 IP"
	} else if output.Verified {
		status = "ready"
		message = "代理连通性验证通过"
	}
	if output.Message != "" {
		message = redact(output.Message)
	}
	_ = s.Store.SetStatus(node.ID, status, message, output.ACMEViaCaddy)
	log(status, message)
	s.Store.FinishJob(job, output.Verified || output.ControlOnNode)
	if job.Kind == "install" && (output.Verified || output.ControlOnNode) {
		if _, ok := s.runner.(networkQualityRunner); ok {
			// Commit installation first: a crash during an optional diagnostic
			// must not replay a successful install after its password was erased.
			current, readErr := s.Store.Node(node.ID)
			if readErr == nil && current.Lifecycle == "active" {
				_, queueErr := s.Store.QueueNetworkQuality(node.ID, password, current.Revision)
				if queueErr == nil {
					log("quality", "已排队检测节点网络质量，代理可继续使用")
					s.signal()
				} else {
					log("quality", "自动网络质量检测未排队，可在节点详情中手动检测")
				}
			}
		}
	}
}
