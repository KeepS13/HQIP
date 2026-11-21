package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/secure"
	"github.com/apernet/hysteria/core/v2/billing"
)

type realityBillingTicket struct {
	ClientIP string `json:"clientIP"`
	Node     string `json:"node"`
	Password string `json:"password"`
	Nonce    string `json:"nonce"`
	Expires  int64  `json:"expires"`
}

func (agent *Agent) realityBillingPassword(password, clientIP string) (string, error) {
	ip, err := model.CanonicalClientIP(clientIP)
	if err != nil {
		return "", err
	}
	// Bind the original Reality peer before tunnelling through local HY2. A
	// client cannot forge this address without the node's signing secret.
	ticket := realityBillingTicket{ClientIP: ip, Node: agent.cfg.NodeID, Password: password, Nonce: secure.Token(), Expires: agent.now().Add(30 * time.Second).Unix()}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(agent.cfg.Token))
	mac.Write(raw)
	return "hqip-reality." + base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (agent *Agent) billingIdentity(password, capability, clientIP string) (string, string, string, error) {
	if !strings.HasPrefix(password, "hqip-reality.") {
		if capability != billing.Capability {
			return password, "", clientIP, nil
		}
		return password, "hysteria2", clientIP, nil
	}
	parts := strings.Split(password, ".")
	if len(parts) != 3 || capability != billing.Capability {
		return "", "", "", errors.New("invalid internal credential")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", "", err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", "", err
	}
	mac := hmac.New(sha256.New, []byte(agent.cfg.Token))
	mac.Write(raw)
	var ticket realityBillingTicket
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(raw, &ticket) != nil || ticket.Node != agent.cfg.NodeID || len(ticket.Password) != 43 || ticket.Expires < agent.now().Unix() || ticket.Expires > agent.now().Add(time.Minute).Unix() {
		return "", "", "", errors.New("invalid internal credential")
	}
	ip, err := model.CanonicalClientIP(ticket.ClientIP)
	if err != nil {
		return "", "", "", err
	}
	return ticket.Password, "vless", ip, nil
}
