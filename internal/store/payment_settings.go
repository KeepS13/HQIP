package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/payment"
)

var ErrPaymentDisabled = errors.New("在线支付未启用，请联系管理员")
var ErrPaymentNotify = errors.New("支付通知校验失败")
var ErrPaymentQueryDisabled = errors.New("未启用主动查单；请在支付平台核对并重新发送通知")

type paymentConfiguration struct {
	Version         int64
	Identity        string
	Config          payment.Config
	QueryEnabled    bool
	EncryptedSecret string
}

func paymentIdentity(base, merchant string) string {
	sum := sha256.Sum256([]byte(base + "\n" + merchant))
	return hex.EncodeToString(sum[:])
}

func readPaymentConfiguration(q portalQuery, version int64) (paymentConfiguration, error) {
	c := paymentConfiguration{Version: version}
	err := q.QueryRow("SELECT api_base_url,merchant_num,pay_type,pay_duration_minutes,query_enabled,secret,identity FROM payment_config_versions WHERE version=? AND revoked=0", version).Scan(&c.Config.APIBaseURL, &c.Config.MerchantNum, &c.Config.PayType, &c.Config.PayDurationMinutes, &c.QueryEnabled, &c.EncryptedSecret, &c.Identity)
	return c, notFound(err)
}

func (s *Store) PaymentSettings() (model.PaymentSettings, error) {
	out := model.PaymentSettings{PayType: "aloop", PayDurationMinutes: 10, NotifyURL: s.BaseURL + "/api/v1/payments/fm/notify"}
	var version int64
	err := s.DB.QueryRow("SELECT enabled,current_version,revision FROM payment_settings WHERE id=1").Scan(&out.Enabled, &version, &out.Revision)
	if err != nil {
		return out, err
	}
	if version == 0 {
		return out, nil
	}
	c, err := readPaymentConfiguration(s.DB, version)
	if err != nil {
		return out, err
	}
	out.APIBaseURL = c.Config.APIBaseURL
	out.MerchantNum = c.Config.MerchantNum
	out.PayType = c.Config.PayType
	out.PayDurationMinutes = c.Config.PayDurationMinutes
	out.QueryEnabled = c.QueryEnabled
	out.SecretConfigured = c.EncryptedSecret != ""
	return out, nil
}

func (s *Store) SavePaymentSettings(in model.PaymentSettingsInput, actor string) (model.PaymentSettings, error) {
	if in.PayType == "" {
		in.PayType = "aloop"
	}
	if in.PayDurationMinutes == 0 {
		in.PayDurationMinutes = 10
	}
	if in.PayType != "aloop" && in.PayType != "alipay" || in.PayDurationMinutes < 1 || in.PayDurationMinutes > 15 {
		return model.PaymentSettings{}, errors.New("支付通道或有效期无效")
	}
	var err error
	if in.APIBaseURL != "" {
		in.APIBaseURL, err = payment.NormalizeBaseURL(strings.TrimSpace(in.APIBaseURL))
		if err != nil {
			return model.PaymentSettings{}, err
		}
	}
	in.MerchantNum = strings.TrimSpace(in.MerchantNum)
	err = retryDatabaseError(func() error {
		tx, e := s.DB.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		var revision, current int64
		if e = tx.QueryRow("SELECT revision,current_version FROM payment_settings WHERE id=1").Scan(&revision, &current); e != nil {
			return e
		}
		if revision != in.ExpectedRevision {
			return ErrConflict
		}
		previous := paymentConfiguration{}
		if current != 0 {
			previous, e = readPaymentConfiguration(tx, current)
			if e != nil {
				return e
			}
		}
		identity := paymentIdentity(in.APIBaseURL, in.MerchantNum)
		encrypted := previous.EncryptedSecret
		if identity != previous.Identity && current != 0 && in.Secret == nil {
			return errors.New("更换支付接口或商户号时必须重新填写密钥")
		}
		secret := ""
		if in.Secret != nil {
			secret = *in.Secret
			encrypted = ""
			if secret != "" {
				encrypted, e = s.Vault.Encrypt(secret)
				if e != nil {
					return e
				}
			}
		} else if encrypted != "" {
			secret, e = s.Vault.Decrypt(encrypted)
			if e != nil {
				return e
			}
		}
		if in.Enabled || secret != "" {
			if e = payment.ValidateConfig(payment.Config{APIBaseURL: in.APIBaseURL, MerchantNum: in.MerchantNum, Secret: secret, PayType: in.PayType, PayDurationMinutes: in.PayDurationMinutes}); e != nil {
				return e
			}
		}
		if in.Enabled && secret == "" {
			return errors.New("启用支付前请配置商户接口及密钥")
		}
		// Claim the settings revision before inserting its immutable config.
		// Concurrent editors then produce a serializable retry / ErrConflict,
		// never an unrelated 23505 on the version primary key.
		result, e := tx.Exec("UPDATE payment_settings SET enabled=?,current_version=?,revision=revision+1,updated_at=? WHERE id=1 AND revision=?", in.Enabled, revision+1, s.Now().Unix(), revision)
		if e != nil {
			return e
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return ErrConflict
		}
		if _, e = tx.Exec("INSERT INTO payment_config_versions(version,api_base_url,merchant_num,pay_type,pay_duration_minutes,query_enabled,secret,identity,created_at) VALUES(?,?,?,?,?,?,?,?,?)", revision+1, in.APIBaseURL, in.MerchantNum, in.PayType, in.PayDurationMinutes, in.QueryEnabled, encrypted, identity, s.Now().Unix()); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO audit(at,action,subject) VALUES(?,'payments.settings',?)", s.Now().Unix(), fmt.Sprintf("%s revision:%d", actor, revision+1)); e != nil {
			return e
		}
		return tx.Commit()
	})
	if err != nil {
		return model.PaymentSettings{}, err
	}
	return s.PaymentSettings()
}

func (s *Store) decryptPaymentConfiguration(c paymentConfiguration) (paymentConfiguration, error) {
	secret, err := s.Vault.Decrypt(c.EncryptedSecret)
	if err != nil {
		return c, err
	}
	c.Config.Secret = secret
	return c, nil
}

func paymentActivity(tx *sql.Tx, order, kind, message string, at int64) error {
	_, err := tx.Exec("INSERT INTO payment_activities(id,order_id,at,kind,message) VALUES(?,?,?,?,?)", NewID(), order, at, kind, message)
	return err
}
