package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/apernet/hysteria/core/v2/billing"
)

var errOfflineDenied = errors.New("cached node authorization unavailable")

type offlineChild struct {
	Billing             map[int64]billing.Segment `json:"billing,omitempty"`
	BillingAcknowledged []billing.Segment         `json:"billingAcknowledged,omitempty"`
	NormalUsed          int64                     `json:"normalUsed,omitempty"`
	EmergencyUsed       int64                     `json:"emergencyUsed,omitempty"`
	CoreGeneration      string                    `json:"coreGeneration"`
	Ended               bool                      `json:"ended"`
	ID                  string                    `json:"id"`
	Normal              int64                     `json:"normal"`
	Emergency           int64                     `json:"emergency"`
	Upload              int64                     `json:"upload"`
	Download            int64                     `json:"download"`
	AppliedRate         int64                     `json:"appliedRate"`
	Until               time.Time                 `json:"until"`
	Closed              bool                      `json:"closed"`
}

func (c offlineChild) emergencyUsed() int64 {
	if c.Billing != nil {
		return c.EmergencyUsed
	}
	return max(int64(0), c.Upload+c.Download-c.Normal)
}

type offlineState struct {
	BillingReported map[int64]billing.Segment `json:"billingReported,omitempty"`
	BillingPolicies map[int64]billing.Policy  `json:"billingPolicies,omitempty"`
	Grant           model.OfflineGrant        `json:"grant"`
	Children        map[string]offlineChild   `json:"children"`
	LastWall        time.Time                 `json:"lastWall"`
	Denied          bool                      `json:"denied"`
	Online          bool                      `json:"online"`
	RetryAt         time.Time                 `json:"retryAt"`
	Failures        int                       `json:"failures"`
	NextVersion     int64                     `json:"nextVersion"`
	Pending         *model.OfflineUsage       `json:"pending,omitempty"`
}

func (l *ledger) offlineStates() ([]offlineState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.db.Query("SELECT state FROM offline_grants ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []offlineState
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var s offlineState
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func (l *ledger) saveOffline(s offlineState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	// Never evict an unacknowledged grant to make room for another one.
	if len(b) > 4<<20 {
		return errors.New("offline ledger capacity exceeded")
	}
	var count int
	if err = l.db.QueryRow("SELECT COUNT(*) FROM offline_grants WHERE id=?", s.Grant.ID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if err = l.db.QueryRow("SELECT COUNT(*) FROM offline_grants").Scan(&count); err != nil {
			return err
		}
		if count >= maxLeaseQueueRows {
			return errors.New("offline grant capacity exceeded")
		}
	}
	_, err = l.db.Exec("INSERT INTO offline_grants(id,state) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state", s.Grant.ID, string(b))
	return err
}

func (l *ledger) offlineChild(id string) (offlineState, bool, error) {
	states, err := l.offlineStates()
	if err != nil {
		return offlineState{}, false, err
	}
	for _, s := range states {
		if _, ok := s.Children[id]; ok {
			return s, true, nil
		}
	}
	return offlineState{}, false, nil
}

func validOfflineGrant(g model.OfflineGrant, node, hash string, now time.Time) bool {
	return g.Version == model.OfflineAdmissionVersion && g.NodeID == node && g.CredentialHash == hash && leaseIdentifier.MatchString(g.ID) && g.UserID != "" && g.SubscriptionID != "" && g.SessionID != "" && !g.AuthorizedAt.After(now.Add(time.Minute)) && g.OfflineUntil.After(now) && !g.OfflineUntil.After(g.AuthorizedAt.Add(model.OfflineGrace)) && g.NormalBudgetBytes >= 0 && g.EmergencyBudgetBytes >= 0 && g.EmergencyBudgetBytes <= model.EmergencyAllowanceBytes && g.NormalBudgetBytes <= math.MaxInt64-g.EmergencyBudgetBytes && g.UploadBytes >= 0 && g.DownloadBytes >= 0 && g.UploadBytes <= math.MaxInt64-g.DownloadBytes && g.EmergencyUsedBytes >= 0 && g.EmergencyUsedBytes <= g.EmergencyBudgetBytes && g.RateVersion >= 0 && g.BandwidthBytesPerSecond >= 0 && g.Revision >= 0 && !g.Closed && !g.Revoked
}

func (s *offlineState) observe(now time.Time) error {
	if now.Before(s.LastWall) {
		s.Denied = true
		return errOfflineDenied
	}
	s.LastWall = now
	if s.Denied || s.Grant.Closed || s.Grant.Revoked || !now.Before(s.Grant.OfflineUntil) {
		return errOfflineDenied
	}
	return nil
}

func (s *offlineState) usage(now time.Time) (model.OfflineUsage, error) {
	u := model.OfflineUsage{Version: s.NextVersion + 1, AppliedRateVersion: s.Grant.RateVersion}
	allClosed := true
	for _, c := range s.Children {
		if c.Upload < 0 || c.Download < 0 || c.Upload > math.MaxInt64-c.Download || u.UploadBytes > math.MaxInt64-c.Upload || u.DownloadBytes > math.MaxInt64-c.Download {
			return u, errors.New("offline counter overflow")
		}
		u.UploadBytes += c.Upload
		u.DownloadBytes += c.Download
		u.EmergencyUsedBytes += c.emergencyUsed()
		if !c.Closed {
			allClosed = false
			if !c.Ended {
				u.Active = now.Before(c.Until) || u.Active
				u.AppliedRateVersion = min(u.AppliedRateVersion, c.AppliedRate)
			}
		}
	}
	if u.UploadBytes > math.MaxInt64-u.DownloadBytes {
		return u, errors.New("offline counter overflow")
	}
	u.Closed = allClosed && (s.Denied || s.Grant.Revoked || !now.Before(s.Grant.OfflineUntil))
	if s.Grant.Billing != nil {
		segments := map[int64]billing.Segment{}
		for _, child := range s.Children {
			for version, segment := range child.Billing {
				merged := segments[version]
				merged.Version = version
				if merged.AppliedAt.IsZero() || segment.AppliedAt.Before(merged.AppliedAt) {
					merged.AppliedAt = segment.AppliedAt
				}
				if segment.ValidUntil.After(merged.ValidUntil) {
					merged.ValidUntil = segment.ValidUntil
				}
				merged.Upload += segment.Upload
				merged.Download += segment.Download
				merged.EmergencyUpload += segment.EmergencyUpload
				merged.EmergencyDownload += segment.EmergencyDownload
				segments[version] = merged
			}
		}
		for _, segment := range segments {
			if !segment.Equal(s.BillingReported[segment.Version]) {
				u.Billing = append(u.Billing, segment)
			}
		}
		sort.Slice(u.Billing, func(left, right int) bool { return u.Billing[left].Version < u.Billing[right].Version })
	}
	return u, nil
}

func (s *offlineState) updateChild(id string, u model.LeaseUsage, now time.Time) error {
	if s.Grant.Billing != nil {
		return s.updateBillingChild(id, u)
	}
	c, ok := s.Children[id]
	if !ok {
		return sql.ErrNoRows
	}
	if u.UploadBytes < c.Upload || u.DownloadBytes < c.Download || u.UploadBytes > math.MaxInt64-u.DownloadBytes || u.UploadBytes+u.DownloadBytes > c.Normal+c.Emergency || u.AppliedRateVersion < c.AppliedRate || u.AppliedRateVersion > s.Grant.RateVersion || c.Closed && (!u.Closed || u.UploadBytes != c.Upload || u.DownloadBytes != c.Download) {
		return errLeaseCounterRegression
	}
	c.Upload, c.Download, c.AppliedRate = u.UploadBytes, u.DownloadBytes, u.AppliedRateVersion
	if u.Closed {
		// Only an explicit final can return delegated, unspent bytes.
		emergency := max(int64(0), c.Upload+c.Download-c.Normal)
		c.Normal = min(c.Normal, c.Upload+c.Download)
		c.Emergency = emergency
		c.Closed = true
	}
	s.Children[id] = c
	return nil
}

func (s *offlineState) localGrant(id string, now time.Time) (model.ConnectionLease, error) {
	if s.Grant.Billing != nil {
		return s.localBillingGrant(id, now)
	}
	var empty model.ConnectionLease
	if err := s.observe(now); err != nil {
		return empty, err
	}
	c, ok := s.Children[id]
	if !ok {
		if len(s.Children) >= 4096 {
			return empty, errors.New("offline child capacity exceeded")
		}
		for _, other := range s.Children {
			if !other.Closed && !other.Ended {
				return empty, errOfflineDenied
			}
		}
		c = offlineChild{ID: id}
	} else if c.Closed || c.Ended || !now.Before(c.Until) {
		return empty, errOfflineDenied
	}
	var normal, emergency int64
	for _, other := range s.Children {
		if normal > math.MaxInt64-other.Normal {
			return empty, errOfflineDenied
		}
		normal += other.Normal
		emergency += other.Emergency
	}
	remaining := c.Normal + c.Emergency - c.Upload - c.Download
	need := max(int64(0), model.OfflineChunkBytes-remaining)
	available := max(int64(0), s.Grant.NormalBudgetBytes-normal)
	if s.Grant.NormalUnlimited {
		available = need
	}
	// Normal bytes cannot be appended behind an already-spent emergency tail:
	// the per-child boundary is immutable once emergency bytes are delegated.
	if c.Emergency == 0 {
		add := min(need, available)
		c.Normal += add
		need -= add
	}
	if !s.Online && need > 0 {
		add := min(need, max(int64(0), s.Grant.EmergencyBudgetBytes-emergency))
		c.Emergency += add
	}
	if s.Online && c.Upload+c.Download >= c.Normal {
		return empty, errOfflineDenied
	}
	if c.Normal+c.Emergency <= c.Upload+c.Download {
		return empty, errOfflineDenied
	}
	c.Until = now.Add(120 * time.Second)
	if c.Until.After(s.Grant.OfflineUntil) {
		c.Until = s.Grant.OfflineUntil
	}
	s.Children[id] = c
	return model.ConnectionLease{ID: id, LeaseID: id, UserID: s.Grant.UserID, SubscriptionID: s.Grant.SubscriptionID, NodeID: s.Grant.NodeID, AccountingID: "subscription_" + s.Grant.SubscriptionID, ExpiresAt: c.Until, BudgetBytes: c.Normal + c.Emergency, ConsumedBytes: c.Upload + c.Download, UploadBytes: c.Upload, DownloadBytes: c.Download, Revision: s.Grant.Revision, RateVersion: s.Grant.RateVersion, BandwidthBytesPerSecond: s.Grant.BandwidthBytesPerSecond, RatePending: s.Grant.RatePending}, nil
}
