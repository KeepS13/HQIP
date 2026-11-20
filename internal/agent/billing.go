package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/apernet/hysteria/core/v2/billing"
)

type realityBillingTicket struct {
	Node     string `json:"node"`
	Password string `json:"password"`
	Nonce    string `json:"nonce"`
	Expires  int64  `json:"expires"`
}

func (agent *Agent) realityBillingPassword(password string) (string, error) {
	ticket := realityBillingTicket{Node: agent.cfg.NodeID, Password: password, Nonce: secure.Token(), Expires: agent.now().Add(30 * time.Second).Unix()}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(agent.cfg.Token))
	mac.Write(raw)
	return "hqip-reality." + base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (agent *Agent) billingIdentity(password, capability string) (string, string, error) {
	if !strings.HasPrefix(password, "hqip-reality.") {
		if capability != billing.Capability {
			return password, "", nil
		}
		return password, "hysteria2", nil
	}
	parts := strings.Split(password, ".")
	if len(parts) != 3 || capability != billing.Capability {
		return "", "", errors.New("invalid internal credential")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", err
	}
	mac := hmac.New(sha256.New, []byte(agent.cfg.Token))
	mac.Write(raw)
	var ticket realityBillingTicket
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(raw, &ticket) != nil || ticket.Node != agent.cfg.NodeID || len(ticket.Password) != 43 || ticket.Expires < agent.now().Unix() || ticket.Expires > agent.now().Add(time.Minute).Unix() {
		return "", "", errors.New("invalid internal credential")
	}
	return ticket.Password, "vless", nil
}
