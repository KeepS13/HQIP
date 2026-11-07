package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
)

type ServerExpiryRequest struct {
	ExpectedRevision int64     `json:"expectedRevision"`
	RequestID        string    `json:"requestId"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

// CorrectServerExpiry keeps the current cycle and usage while replacing only
// future boundaries. Its request keys share the renewal ledger for safe retries.
func (s *Store) CorrectServerExpiry(server string, request ServerExpiryRequest, actor string) (model.Server, model.ServerRenewal, error) {
	var empty model.Server
	var record model.ServerRenewal
	if !validRequestKey(request.RequestID) || request.RequestID == "initial" || request.ExpectedRevision < 1 {
		return empty, record, errors.New("修改到期时间需要有效版本和请求标识")
	}
	request.ExpiresAt = request.ExpiresAt.UTC()
	payload := jsonString(struct {
		Kind      string    `json:"kind"`
		ExpiresAt time.Time `json:"expiresAt"`
	}{"correction", request.ExpiresAt})
	tx, err := s.DB.Begin()
	if err != nil {
		return empty, record, err
	}
	defer tx.Rollback()
	var storedPayload, raw string
	err = tx.QueryRow("SELECT payload,body FROM server_renewals WHERE server_id=? AND request_id=?", server, request.RequestID).Scan(&storedPayload, &raw)
	if err == nil {
		if storedPayload != payload {
			return empty, record, ErrConflict
		}
		if err = json.Unmarshal([]byte(raw), &record); err != nil {
			return empty, record, err
		}
		if err = tx.Commit(); err != nil {
			return empty, record, err
		}
		v, e := s.Server(server)
		return v, record, e
	}
	if err != sql.ErrNoRows {
		return empty, record, err
	}
	var revision int64
	if err = tx.QueryRow("SELECT revision FROM servers WHERE id=? AND deleted_at=0", server).Scan(&revision); err != nil {
		return empty, record, notFound(err)
	}
	if revision != request.ExpectedRevision {
		return empty, record, ErrConflict
	}
	b, err := billingStateFor(tx, server)
	if err == sql.ErrNoRows {
		return empty, record, errors.New("请先配置服务器到期时间")
	}
	if err != nil {
		return empty, record, err
	}
	now := s.Now()
	if !validServerExpiry(request.ExpiresAt, now) {
		return empty, record, errors.New("请选择未来的北京时间整点到期时间")
	}
	previous := time.Unix(b.expires, 0).UTC()
	record = model.ServerRenewal{ID: NewID(), ServerID: server, RequestID: request.RequestID, Kind: "correction", PreviousExpiresAt: &previous, ExpiresAt: request.ExpiresAt, ConfirmedAt: now.UTC(), Actor: actor, Revision: revision + 1}
	if err = reanchorServerBilling(tx, server, b, request.ExpiresAt, now); err != nil {
		return empty, record, err
	}
	if _, err = tx.Exec("UPDATE servers SET revision=revision+1 WHERE id=?", server); err != nil {
		return empty, record, err
	}
	if err = recordServerRenewal(tx, record, payload); err != nil {
		return empty, record, err
	}
	if err = cacheServerBilling(tx, server, now); err != nil {
		return empty, record, err
	}
	if err = tx.Commit(); err != nil {
		return empty, record, err
	}
	v, err := s.Server(server)
	return v, record, err
}
