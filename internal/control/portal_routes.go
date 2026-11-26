package control

import "net/http"

// Portal routes keep browser, administrator and native client authentication separate.
func (s *Server) registerPortalAdmin(m *http.ServeMux) {
	s.registerPaymentAdmin(m)
	for pattern, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/settings/traffic-billing": s.trafficBillingSettings, "PUT /api/v1/settings/traffic-billing": s.trafficBillingSettings,
		"GET /api/v1/subscription-templates": s.subscriptionTemplates, "POST /api/v1/subscription-templates": s.saveSubscriptionTemplate, "PUT /api/v1/subscription-templates/{id}": s.saveSubscriptionTemplate, "POST /api/v1/subscription-templates/{id}/preview": s.previewSubscriptionTemplate,
		"GET /api/v1/subscription-requests": s.adminSubscriptionRequests, "POST /api/v1/subscription-requests/{id}/approve": stoppedApplication, "POST /api/v1/subscription-requests/{id}/reject": stoppedApplication,
		"GET /api/v1/users/{id}/subscriptions": s.adminUserSubscriptions, "POST /api/v1/users/{id}/subscriptions": s.adminAssignSubscription, "DELETE /api/v1/users/{id}/subscriptions/{subscriptionId}": s.adminRemoveSubscription,
		"PATCH /api/v1/users/{id}/subscriptions/{subscriptionId}/expiry": s.adminSubscriptionExpiry,
		"GET /api/v1/users/{id}/subscriptions/{subscriptionId}/nodes":    s.adminSubscriptionNodes,
		"PATCH /api/v1/users/{id}/subscriptions/{subscriptionId}/nodes":  s.adminSubscriptionNodes,
		"GET /api/v1/users": s.portalUsers, "GET /api/v1/users/{id}": s.portalAdminUser, "PATCH /api/v1/users/{id}": s.portalAdminEnabled, "POST /api/v1/users/{id}/password": s.portalAdminPassword,
		"POST /api/v1/users/{id}/sessions/{sessionId}/revoke": s.portalAdminRevokeSession, "POST /api/v1/users/{id}/connections/{leaseId}/revoke": s.portalAdminRevokeConnection,
		"GET /api/v1/settings/turnstile": s.portalTurnstileSettings, "PUT /api/v1/settings/turnstile": s.portalTurnstileSettings, "GET /api/v1/settings/portal": s.portalSettings, "PUT /api/v1/settings/portal": s.portalSettings, "POST /api/v1/settings/portal/activate": s.portalActivate,
		"GET /api/v1/settings/portal/recovery": s.portalRecovery, "POST /api/v1/settings/portal/recovery/resume": s.portalResume,
	} {
		m.Handle(pattern, s.auth(handler, false))
	}
}
func (s *Server) registerPortalPublic(m *http.ServeMux) {
	s.registerPaymentPublic(m)
	m.HandleFunc("GET /api/v1/user/config", s.portalConfig)
	m.HandleFunc("POST /api/v1/user/register", s.portalRegister)
	m.HandleFunc("POST /api/v1/user/login", s.portalLogin)
	m.HandleFunc("GET /api/v1/user/session", s.portalSession)
	m.HandleFunc("GET /api/v1/user/releases", s.portalReleases)
	m.HandleFunc("GET /api/v1/user/installers", s.clientInstallers)
	m.HandleFunc("GET /downloads/client/{sha256}/{filename}", s.clientInstallerDownload)
	m.HandleFunc("GET /api/v1/user/download", s.portalDownloadInfo)
	m.HandleFunc("GET /api/v3/client/releases", s.godotReleases)
	m.HandleFunc("GET /downloads/godot/{sha256}/{filename}", s.godotDownload)
	m.HandleFunc("GET /downloads/flutter/preview/{sha256}/{filename}", s.flutterDownload)
	m.HandleFunc("GET /downloads/HQIP-Windows-x64.zip", s.portalDownload)
	m.HandleFunc("GET /downloads/{sha256}/HQIP-Windows-x64.zip", s.portalDownload)
	for pattern, handler := range map[string]portalHandler{
		"GET /api/v1/user/profile": s.portalProfile, "POST /api/v1/user/logout": s.portalLogout, "POST /api/v1/user/password": s.portalPassword,
		"GET /api/v1/user/catalog": s.userCatalog, "GET /api/v1/user/subscription-requests": s.userSubscriptionRequests,
		"GET /api/v1/user/import-link": s.portalSubscription, "POST /api/v1/user/import-link": s.portalSubscription, "POST /api/v1/user/import-link/rotate": s.portalSubscription, "POST /api/v1/user/import-link/revoke": s.portalSubscription,
		"GET /api/v1/user/subscriptions/{id}/import-link": s.portalSubscription,
		"GET /api/v1/user/subscriptions":                  s.userSubscriptions, "GET /api/v1/user/subscriptions/{id}": s.userSubscriptionDetail,
		"POST /api/v1/user/subscriptions/{id}/import-link": s.portalSubscription, "POST /api/v1/user/subscriptions/{id}/import-link/rotate": s.portalSubscription, "POST /api/v1/user/subscriptions/{id}/import-link/revoke": s.portalSubscription,
		"GET /api/v1/user/sessions": s.portalSessions, "POST /api/v1/user/sessions/{id}/revoke": s.portalRevokeSession,
		"GET /api/v1/user/authorize/{id}": s.portalAuthorize, "POST /api/v1/user/authorize/{id}": s.portalAuthorize,
	} {
		m.Handle(pattern, s.portalAuth("browser", handler))
	}
	m.HandleFunc("GET /usub/{token}", s.portalYAML)
	m.HandleFunc("/api/v2/client/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusUpgradeRequired, "请升级 HQIP 多订阅客户端后重新登录")
	})
	m.HandleFunc("POST /api/v3/client/authorize", s.nativeBegin)
	m.HandleFunc("POST /api/v3/client/token", s.nativeToken)
	m.HandleFunc("POST /api/v3/client/refresh", s.nativeRefresh)
	for pattern, handler := range map[string]portalHandler{
		"POST /api/v3/client/logout": s.portalLogout, "GET /api/v3/client/profile": s.nativeProfile, "POST /api/v3/client/subscription": s.nativeSelectSubscription, "POST /api/v3/client/status": s.nativeStatus, "GET /api/v3/client/rules/{hash}": s.nativeRules,
		"GET /api/v3/client/connections": s.portalConnections, "POST /api/v3/client/connections/drain": s.nativeDrain,
		"GET /api/v3/client/billing": s.nativeBilling,
		"GET /api/v3/client/changes": s.nativeChanges,
	} {
		m.Handle(pattern, s.portalAuth("native", handler))
	}
	m.HandleFunc("POST /api/v1/agents/offline-grants", s.agentOfflineAdmission)
	m.HandleFunc("POST /api/v1/agents/offline-grants/recover", s.agentOfflineRecovery)
	m.HandleFunc("POST /api/v1/agents/offline-grants/{id}/report", s.agentOfflineReport)
	m.HandleFunc("POST /api/v1/agents/admission", s.agentAdmission)
	m.HandleFunc("POST /api/v1/agents/admission/check", s.agentAdmissionIdentity)
	m.HandleFunc("POST /api/v1/agents/reality-credential", s.agentRealityCredential)
	m.HandleFunc("POST /api/v1/agents/leases/{id}/renew", s.agentLeaseRenew)
}
