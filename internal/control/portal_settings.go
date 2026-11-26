package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/rules"
)

// Portal settings and Turnstile verification share the existing server policy.
func (s *Server) portalConfig(w http.ResponseWriter, r *http.Request) {
	cfg, _ := s.Store.TurnstileSettings()
	// Public routing guidance uses the same exact hosts as subscription export;
	// it carries no credentials and does not extend trusted authentication origins.
	reply(w, 200, map[string]any{
		"registrationEnabled": s.Store.PortalSetting("portal_registration") == "true",
		"portalEnabled":       s.Store.PortalEnabled(), "recoveryHold": s.Store.PortalSetting("portal_recovery_hold") == "true",
		"turnstileSiteKey": cfg.SiteKey, "turnstileConfigured": cfg.SecretConfigured,
		"directAccessHosts": rules.ServiceBypassHosts(s.opts.PublicURL, s.opts.UserOrigin, s.opts.AdminOrigin),
	})
}
func (s *Server) portalSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var b struct {
			RegistrationEnabled bool `json:"registrationEnabled"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		value := "false"
		if b.RegistrationEnabled {
			value = "true"
		}
		if err := s.Store.SetPortalSetting("portal_registration", value); err != nil {
			portalResult(w, nil, err)
			return
		}
	}
	s.portalConfig(w, r)
}
func (s *Server) portalTurnstileSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var b struct {
			SiteKey          string   `json:"siteKey"`
			SecretKey        *string  `json:"secretKey"`
			AllowedHostnames []string `json:"allowedHostnames"`
			ExpectedRevision int64    `json:"expectedRevision"`
		}
		if !readJSON(w, r, &b) {
			return
		}
		if len(b.AllowedHostnames) == 0 {
			base, _ := url.Parse(s.opts.UserOrigin)
			b.AllowedHostnames = []string{base.Hostname()}
		}
		v, err := s.Store.SaveTurnstile(b.SiteKey, b.AllowedHostnames, b.SecretKey, b.ExpectedRevision)
		portalResult(w, v, err)
		return
	}
	v, e := s.Store.TurnstileSettings()
	portalResult(w, v, e)
}
func (s *Server) verifyTurnstile(ctx context.Context, token, ip, action string) bool {
	if len(token) < 1 || len(token) > 2048 {
		return false
	}
	settings, secret, err := s.Store.TurnstileVerificationConfig()
	if err != nil || secret == "" || settings.SiteKey == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	form := url.Values{"secret": {secret}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := s.opts.TurnstileClient
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		client = &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false
	}
	var answer struct {
		Success     bool      `json:"success"`
		Hostname    string    `json:"hostname"`
		Action      string    `json:"action"`
		ChallengeTS time.Time `json:"challenge_ts"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 32768)).Decode(&answer) != nil {
		return false
	}
	base, _ := url.Parse(s.opts.UserOrigin)
	now := s.Store.Now()
	allowed := settings.AllowedHostnames
	if len(allowed) == 0 {
		allowed = []string{base.Hostname()}
	}
	hostOK := false
	for _, h := range allowed {
		if strings.EqualFold(h, answer.Hostname) {
			hostOK = true
		}
	}
	if !answer.Success || !hostOK || answer.Action != action || answer.ChallengeTS.Before(now.Add(-5*time.Minute)) || answer.ChallengeTS.After(now.Add(time.Minute)) {
		return false
	}
	return s.Store.ConsumeTurnstile(token)
}
