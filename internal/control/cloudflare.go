package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/certificates"
	"github.com/KeepS13/HQIP/internal/cloudflare"
	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/KeepS13/HQIP/internal/store"
)

var errCertificateBackoff = errors.New("上次证书签发失败，正在等待退避重试")

func (s *Server) registerCloudflareRoutes(mux *http.ServeMux) {
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/settings/cloudflare":              s.getCloudflareSettings,
		"PUT /api/v1/settings/cloudflare":              s.putCloudflareSettings,
		"POST /api/v1/settings/cloudflare/verify":      s.verifyCloudflareSettings,
		"GET /api/v1/settings/cloudflare/zones":        s.cloudflareZones,
		"GET /api/v1/settings/cloudflare/certificates": s.cloudflareCertificates,
		"GET /api/v1/settings/cloudflare/dns":          s.cloudflareDNS,
	} {
		mux.Handle(pattern, s.auth(handler, false))
	}
}
func (s *Server) getCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.CloudflareSettings()
	result(w, out, err)
}
func (s *Server) cloudflareRequestClient(token string) (*cloudflare.Client, error) {
	if token == "" {
		var err error
		token, err = s.Store.CloudflareToken()
		if err != nil {
			return nil, errors.New("请先配置 Cloudflare Token")
		}
	}
	if err := cloudflare.ValidateToken(token); err != nil {
		return nil, err
	}
	return cloudflare.New(token), nil
}
func (s *Server) putCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	var in store.CloudflareSettingsInput
	if !readJSON(w, r, &in) {
		return
	}
	address, err := mail.ParseAddress(in.Email)
	if err != nil || address.Address != in.Email || len(in.Email) > 254 {
		fail(w, 400, "请填写有效的 ACME 联系邮箱")
		return
	}
	client, err := s.cloudflareRequestClient(in.Token)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err = client.Verify(ctx); err != nil {
		fail(w, 400, err.Error())
		return
	}
	zone, err := client.Zone(ctx, in.DefaultZoneID)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if _, err = client.Records(ctx, zone.ID, "_hqip-permission-check."+zone.Name); err != nil {
		fail(w, 400, err.Error())
		return
	}
	out, err := s.Store.SaveCloudflareSettings(in, zone)
	if errors.Is(err, store.ErrConflict) {
		fail(w, 409, err.Error())
		return
	}
	result(w, out, err)
}
func (s *Server) verifyCloudflareSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	client, err := s.cloudflareRequestClient(in.Token)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err = client.Verify(ctx); err != nil {
		fail(w, 400, err.Error())
		return
	}
	zones, err := client.Zones(ctx)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"ok": true, "zones": zones, "dnsWriteVerified": false, "message": "Token 有效；DNS 写权限将在首次创建节点记录时校验"})
}
func (s *Server) cloudflareZones(w http.ResponseWriter, r *http.Request) {
	client, err := s.cloudflareRequestClient("")
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	zones, err := client.Zones(r.Context())
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"zones": zones})
}
func (s *Server) cloudflareCertificates(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.Store.CertificateStatuses()
	result(w, map[string]any{"certificates": statuses}, err)
}
func (s *Server) cloudflareDNS(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.DNSStatuses()
	result(w, map[string]any{"records": items}, err)
}

func (s *Server) prepareNodeCloudflare(ctx context.Context, node model.Node) (model.Node, *model.CertificateBundle, error) {
	if node.DNSMode != "cloudflare" && node.CertificateMode != "cloudflare" {
		return node, nil, nil
	}
	// Bound the full DNS and ACME operation below its lease lifetime, including
	// deferred challenge cleanup, so another worker cannot acquire it mid-issue.
	ctx, cancel := context.WithTimeout(ctx, 18*time.Minute)
	defer cancel()
	lease, err := s.Store.AcquireCloudflareLease("node:"+node.ID, 20*time.Minute)
	if err != nil {
		return node, nil, errors.New("节点域名或证书操作正在进行，请稍后重试")
	}
	defer s.Store.ReleaseCloudflareLease("node:"+node.ID, lease)
	updated, bundle, err := s.prepareCloudflareLocked(ctx, node)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errCertificateBackoff) {
		_ = s.Store.CertificateFailure(node.ID, updated.Domain, err.Error())
	}
	return updated, bundle, err
}
func (s *Server) prepareCloudflareLocked(ctx context.Context, node model.Node) (model.Node, *model.CertificateBundle, error) {
	if ctx.Err() != nil {
		return node, nil, ctx.Err()
	}
	// Refusing a premature retry is not a new failed ACME attempt. Check it
	// before settings/API calls so a repaired Token cannot extend the timer.
	if !s.Store.CertificateRenewalDue(node.ID) {
		cached, err := s.Store.CertificateBundle(node.ID)
		if err != nil {
			return node, nil, err
		}
		if cached == nil || cached.Domain != node.Domain || !s.Store.Now().Before(cached.NotAfter) {
			return node, nil, errCertificateBackoff
		}
	}
	settings, err := s.Store.CloudflareSettings()
	if err != nil {
		return node, nil, err
	}
	if !settings.Configured {
		return node, nil, errors.New("请先在域名与证书页面配置 Cloudflare")
	}
	client, err := s.cloudflareRequestClient("")
	if err != nil {
		return node, nil, err
	}
	zoneID := node.ZoneID
	if zoneID == "" {
		zoneID = settings.DefaultZoneID
	}
	zone, err := client.Zone(ctx, zoneID)
	if err != nil {
		return node, nil, err
	}
	if node.DNSMode == "cloudflare" {
		if ip := net.ParseIP(node.PublicIP); ip == nil || ip.To4() == nil {
			return node, nil, errors.New("Cloudflare 节点需要公网 IPv4 地址")
		}
		var intent store.DNSIntent
		allocated := false
		for attempt := 0; attempt < 5; attempt++ {
			intent, err = s.Store.NodeDNSIntent(node.ID)
			if errors.Is(err, store.ErrNotFound) {
				if node.IPSwitch != nil {
					return node, nil, errors.New("未找到原节点的 Cloudflare DNS 归属记录，无法安全切换")
				}
				name := s.Store.CloudflareName(node.ID, zone.Name)
				intent, err = s.Store.CreateDNSIntent(node.ID, zone.ID, zone.Name, cloudflare.Record{Type: "A", Name: name, Content: node.PublicIP, TTL: 120, Proxied: false, Comment: "HQIP node " + node.ID + " " + secure.Token()[:16]})
			}
			if err != nil {
				return node, nil, err
			}
			if intent.ZoneID == zone.ID && node.IPSwitch != nil && intent.Record.Content == node.IPSwitch.FromIP {
				updated, switchErr := client.SwitchOwnedIPv4(ctx, intent.ZoneID, intent.Record, node.PublicIP)
				if switchErr != nil {
					return node, nil, switchErr
				}
				if err = s.Store.CompleteIPSwitchDNS(intent, updated); err != nil {
					return node, nil, err
				}
				intent.Record = updated
			}
			if intent.ZoneID != zone.ID || intent.Record.Content != node.PublicIP {
				return node, nil, errors.New("节点 DNS 归属已变化，请先清理原记录")
			}
			actual, e := client.EnsureRecord(ctx, intent.ZoneID, intent.Record)
			if errors.Is(e, cloudflare.ErrConflict) && intent.Record.ID == "" {
				if err = s.Store.DNSIntentState(intent.ID, "deleted", "名称冲突，已另选新名称"); err != nil {
					return node, nil, err
				}
				continue
			}
			if e != nil {
				_ = s.Store.DNSIntentState(intent.ID, "error", e.Error())
				return node, nil, e
			}
			if err = s.Store.CompleteDNSIntent(intent.ID, actual); err != nil {
				return node, nil, err
			}
			node.Domain = actual.Name
			node.ZoneID = zone.ID
			node.CertificateMode = "cloudflare"
			allocated = true
			break
		}
		if !allocated {
			return node, nil, cloudflare.ErrConflict
		}
		propagationCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err = cloudflare.WaitPropagation(propagationCtx, zone.Name, node.Domain, "A", node.PublicIP)
		cancel()
		if err != nil {
			return node, nil, err
		}
	}
	if !strings.HasSuffix(strings.ToLower(node.Domain), "."+strings.ToLower(zone.Name)) && !strings.EqualFold(node.Domain, zone.Name) {
		return node, nil, errors.New("节点域名不属于所选 Cloudflare Zone")
	}
	bundle, err := s.Store.CertificateBundle(node.ID)
	if err != nil {
		return node, nil, err
	}
	if bundle != nil && bundle.Domain == node.Domain && s.Store.Now().Before(bundle.NotAfter) && !s.Store.CertificateRenewalDue(node.ID) {
		return node, bundle, nil
	}
	if !s.Store.CertificateRenewalDue(node.ID) {
		return node, nil, errCertificateBackoff
	}
	account, err := s.Store.CertificateAccountKey(certificates.ProductionDirectory)
	if err != nil {
		return node, nil, err
	}
	_ = s.Store.Event(node.ID, "certificate", "正在通过 Cloudflare DNS 验证申请节点证书")
	issuanceCtx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	bundle, err = (certificates.Issuer{Now: s.Store.Now}).Issue(issuanceCtx, account, settings.Email, node.Domain, &nodeChallengeDNS{server: s, client: client, nodeID: node.ID, zone: zone})
	if err != nil {
		return node, nil, err
	}
	if err = s.Store.PublishCertificate(node.ID, bundle); err != nil {
		return node, nil, err
	}
	_ = s.Store.Event(node.ID, "certificate", "节点证书已签发，等待节点应用")
	return node, bundle, nil
}

type nodeChallengeDNS struct {
	server *Server
	client *cloudflare.Client
	nodeID string
	zone   cloudflare.Zone
}

func (d *nodeChallengeDNS) Present(ctx context.Context, name, value string) (string, error) {
	intent, err := d.server.Store.CreateDNSIntent(d.nodeID, d.zone.ID, d.zone.Name, cloudflare.Record{Type: "TXT", Name: name, Content: value, TTL: 120, Comment: "HQIP challenge " + d.nodeID + " " + secure.Token()[:16]})
	if err != nil {
		return "", err
	}
	record, err := d.client.EnsureRecord(ctx, intent.ZoneID, intent.Record)
	if err != nil {
		_ = d.server.Store.DNSIntentState(intent.ID, "cleanup", err.Error())
		return "", err
	}
	if err = d.server.Store.CompleteDNSIntent(intent.ID, record); err != nil {
		return "", err
	}
	return intent.ID, nil
}
func (d *nodeChallengeDNS) Wait(ctx context.Context, name, value string) error {
	return cloudflare.WaitPropagation(ctx, d.zone.Name, name, "TXT", value)
}
func (d *nodeChallengeDNS) Cleanup(ctx context.Context, id string) error {
	intent, err := d.server.Store.DNSIntent(id)
	if err != nil {
		return err
	}
	return d.server.cleanupDNSIntent(ctx, d.client, intent)
}
func (s *Server) cleanupDNSIntent(ctx context.Context, client *cloudflare.Client, intent store.DNSIntent) error {
	if intent.State == "deleted" {
		return nil
	}
	if err := s.Store.DNSIntentState(intent.ID, "cleanup", ""); err != nil {
		return err
	}
	if intent.Record.ID == "" {
		records, err := client.Records(ctx, intent.ZoneID, intent.Record.Name)
		if err != nil {
			return err
		}
		for _, r := range records {
			if r.Comment == intent.Record.Comment {
				if !cloudflare.SameRecord(r, intent.Record) {
					_ = s.Store.DNSIntentState(intent.ID, "cleanup", cloudflare.ErrChanged.Error())
					return cloudflare.ErrChanged
				}
				if intent.Record.ID != "" {
					return cloudflare.ErrChanged
				}
				intent.Record = r
			}
		}
		if intent.Record.ID == "" {
			return s.Store.DNSIntentState(intent.ID, "deleted", "")
		}
	}
	if err := client.DeleteOwned(ctx, intent.ZoneID, intent.Record); err != nil {
		_ = s.Store.DNSIntentState(intent.ID, "cleanup", err.Error())
		return err
	}
	return s.Store.DNSIntentState(intent.ID, "deleted", "")
}
func (s *Server) cleanupNodeDNS(ctx context.Context, node model.Node) error {
	current, err := s.Store.Node(node.ID)
	if err != nil {
		return err
	}
	if current.Lifecycle != "deleted" && (current.Retirement == nil || !current.Retirement.Stopped) {
		return errors.New("节点尚未确认停止，不能清理域名和证书")
	}
	lease, err := s.Store.AcquireCloudflareLease("node:"+node.ID, 20*time.Minute)
	if err != nil {
		return err
	}
	defer s.Store.ReleaseCloudflareLease("node:"+node.ID, lease)
	intents, err := s.Store.NodeDNSIntents(node.ID)
	if err != nil {
		return err
	}
	if len(intents) > 0 {
		client, e := s.cloudflareRequestClient("")
		if e != nil {
			for _, intent := range intents {
				_ = s.Store.DNSIntentState(intent.ID, "cleanup", e.Error())
			}
			return e
		}
		for _, intent := range intents {
			if e = s.cleanupDNSIntent(ctx, client, intent); e != nil {
				return e
			}
		}
	}
	return s.Store.ClearNodeCertificates(node.ID)
}

// A durable lease keeps the minute worker nonblocking and prevents overlapping
// renewals. Each individual operation has its own timeout and node lease.
func (s *Server) cloudflareMaintenance(ctx context.Context) {
	lease, err := s.Store.AcquireCloudflareLease("maintenance", 25*time.Minute)
	if err != nil {
		return
	}
	go func() {
		defer s.Store.ReleaseCloudflareLease("maintenance", lease)
		maintenanceCtx, cancel := context.WithTimeout(ctx, 24*time.Minute)
		defer cancel()
		// A failed pre-install node is a tombstone too, even though it never
		// had an Agent. Its allocated DNS must not be orphaned by list filtering.
		retired, err := s.Store.DeletedCloudflareNodes()
		if err == nil {
			for _, node := range retired {
				if maintenanceCtx.Err() != nil {
					return
				}
				_ = s.cleanupNodeDNS(maintenanceCtx, node)
			}
		}
		client, err := s.cloudflareRequestClient("")
		if err != nil {
			return
		}
		cleanup, err := s.Store.PendingDNSCleanup()
		if err == nil {
			for _, intent := range cleanup {
				if maintenanceCtx.Err() != nil {
					return
				}
				if intent.Kind == "A" {
					node, e := s.Store.Node(intent.NodeID)
					if e != nil || (node.Lifecycle != "deleted" && (node.Retirement == nil || !node.Retirement.Stopped)) {
						continue
					}
				}
				_ = s.cleanupDNSIntent(maintenanceCtx, client, intent)
			}
		}
		nodes, err := s.Store.Nodes()
		if err != nil {
			return
		}
		for _, node := range nodes {
			if maintenanceCtx.Err() != nil {
				return
			}
			if node.Lifecycle != "active" || (node.DNSMode != "cloudflare" && node.CertificateMode != "cloudflare") || !s.Store.CertificateRenewalDue(node.ID) {
				continue
			}
			if node.Status != "ready" && node.Status != "awaiting_external" {
				continue
			}
			_, _, _ = s.prepareNodeCloudflare(maintenanceCtx, node)
		}
	}()
}
