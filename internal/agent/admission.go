package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

var connectionIdentifier = regexp.MustCompile(`^[a-f0-9]{64}$`)
var leaseIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Keep enough time after the four-second control request to persist and flush
// its result. The runtime's local HTTP client allows eight seconds.
const admissionControlTimeout = 4 * time.Second
const admissionWriteTimeout = 6 * time.Second

func (a *Agent) admitConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.localAuthorized(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	var body struct {
		ConnectionID  string `json:"connectionId"`
		Password      string `json:"password"`
		LegacyRenewal string `json:"legacyRenewal,omitempty"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || !connectionIdentifier.MatchString(body.ConnectionID) || len(body.Password) < 16 || len(body.Password) > 1024 {
		http.Error(w, "invalid admission", 400)
		return
	}
	a.cacheMu.RLock()
	portal := a.cache.PortalEnabled
	retired := a.cache.Retirement != nil
	id := ""
	if !retired {
		hash := passwordHash(body.Password)
		for candidate, credential := range a.cache.Credentials {
			if portal && !strings.HasPrefix(candidate, "__probe_") {
				continue
			}
			if _, pending := a.cache.Pending[candidate]; pending || !credential.Enabled || credentialDenied(a.cache, credential.Hash, a.now()) {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(hash), []byte(credential.Hash)) == 1 {
				id = candidate
				break
			}
		}
	}
	if id != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"legacy": true, "accountingId": id})
		a.cacheMu.RUnlock()
		return
	}
	a.cacheMu.RUnlock()
	if retired {
		http.Error(w, "node retired", 403)
		return
	}
	if !portal || body.LegacyRenewal != "" {
		http.Error(w, "portal disabled", 403)
		return
	}
	if a.offlineAllowed() || a.cachedOfflineCredential(body.Password) {
		a.offlineAdmit(w, r, body.ConnectionID, body.Password)
		return
	}
	a.forwardAdmission(w, r, "/api/v1/agents/admission", body)
}

func (a *Agent) renewConnection(w http.ResponseWriter, r *http.Request) {
	a.leaseReportMu.RLock()
	defer a.leaseReportMu.RUnlock()
	w.Header().Set("Cache-Control", "no-store")
	if !a.localAuthorized(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	id := r.PathValue("id")
	if !leaseIdentifier.MatchString(id) {
		http.Error(w, "invalid lease", 400)
		return
	}
	var body model.LeaseUsage
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil || body.UploadBytes < 0 || body.DownloadBytes < 0 || body.AppliedRateVersion < 0 || body.UploadBytes > math.MaxInt64-body.DownloadBytes {
		http.Error(w, "invalid counters", 400)
		return
	}
	a.cacheMu.RLock()
	allowed := a.cache.PortalEnabled && a.cache.Retirement == nil
	a.cacheMu.RUnlock()
	if !allowed && !body.Closed {
		http.Error(w, "node authorization ended", 403)
		return
	}
	if isOfflineLease(id) {
		a.offlineRenew(w, r, id, body)
		return
	}
	pending, err := a.ledger.saveLeaseUsage(id, body, a.now())
	if err != nil {
		status := 503
		if errors.Is(err, errLeaseCounterRegression) {
			status = 409
		}
		http.Error(w, "usage could not be durably accepted", status)
		return
	}
	status, data, err := a.requestAdmission(r.Context(), "/api/v1/agents/leases/"+id+"/renew", body)
	if err == nil && status == 200 && !confirmedLeaseUsage(id, body, data) {
		status = 502
		err = errors.New("lease usage confirmation mismatch")
	}
	if err == nil && status == 200 {
		err = a.ledger.ackLeaseUsage(pending)
	} else {
		_ = a.ledger.retryLeaseUsage(pending, a.now(), status)
	}
	writeAdmissionResponse(w, status, data, err)
}

func (a *Agent) forwardAdmission(w http.ResponseWriter, r *http.Request, path string, body any) {
	// Queueing and the control request share one budget. Once dispatched, learn
	// the outcome even if the caller disconnects, so an unused grant can be closed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), admissionControlTimeout)
	defer cancel()
	// Reserve durable tracking capacity before admitting a real connection. This
	// serializes only admission, never the one-second traffic sampling loop.
	a.admissionMu.Lock()
	defer a.admissionMu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	if ctx.Err() != nil {
		http.Error(w, "admission queue timed out", http.StatusServiceUnavailable)
		return
	}
	a.cacheMu.RLock()
	retired := a.cache.Retirement != nil
	a.cacheMu.RUnlock()
	if retired {
		http.Error(w, "node retired", 403)
		return
	}
	if err := a.ledger.leaseCapacity(a.now()); err != nil {
		http.Error(w, "durable usage capacity unavailable", 503)
		return
	}
	status, data, err := a.requestAdmission(ctx, path, body)
	grantID := ""
	if err == nil && status == 200 {
		var grant struct {
			LeaseID string `json:"leaseId"`
		}
		if json.Unmarshal(data, &grant) != nil || !leaseIdentifier.MatchString(grant.LeaseID) {
			err = errors.New("invalid lease response")
		} else {
			var pending pendingLeaseUsage
			pending, err = a.ledger.saveLeaseUsage(grant.LeaseID, model.LeaseUsage{}, a.now())
			if err == nil {
				// Never close a previously tracked grant because a duplicate
				// admission failed to deliver its response.
				if pending.Version == 1 {
					grantID = grant.LeaseID
				}
				err = a.ledger.ackLeaseUsage(pending)
			}
		}
	}
	deliveryErr := r.Context().Err()
	if deliveryErr == nil {
		deliveryErr = writeAdmissionResponse(w, status, data, err)
	}
	if grantID != "" && (deliveryErr != nil || err != nil) {
		// No usable grant reached the caller. Persist the zero-usage final;
		// the existing retry queue delivers it even across Agent restarts.
		if _, closeErr := a.ledger.saveLeaseUsage(grantID, model.LeaseUsage{Closed: true}, a.now()); closeErr != nil {
			a.log.Error("unable to persist undelivered admission final", "error", closeErr)
		}
	}
}

func (a *Agent) requestAdmission(ctx context.Context, path string, body any) (int, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 400, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.ControlURL+path, bytes.NewReader(data))
	if err != nil {
		return 503, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	resp, err := a.controlClient.Do(req)
	if err != nil {
		return 503, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		status := 503
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			status = resp.StatusCode
		}
		return status, nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	if err != nil || len(b) > 16<<10 || !json.Valid(b) {
		return 502, nil, &admissionProtocolError{}
	}
	return 200, b, nil
}

func writeAdmissionResponse(w http.ResponseWriter, status int, data []byte, err error) error {
	if err != nil {
		http.Error(w, "control unavailable or usage persistence failed", 503)
		return err
	}
	if status != 200 {
		http.Error(w, "admission denied or unavailable", status)
		return nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	if _, err = w.Write(data); err != nil {
		return err
	}
	err = http.NewResponseController(w).Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func confirmedLeaseUsage(id string, usage model.LeaseUsage, data []byte) bool {
	var grant struct {
		LeaseID       string `json:"leaseId"`
		UploadBytes   int64  `json:"uploadBytes"`
		DownloadBytes int64  `json:"downloadBytes"`
		Closed        bool   `json:"closed"`
	}
	return json.Unmarshal(data, &grant) == nil && grant.LeaseID == id && grant.UploadBytes == usage.UploadBytes && grant.DownloadBytes == usage.DownloadBytes && grant.Closed == usage.Closed
}
