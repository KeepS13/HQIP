package agent

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	"github.com/KeepS13/HQIP/internal/reality"
	hy "github.com/apernet/hysteria/core/v2/client"
	utls "github.com/metacubex/utls"
)

type realityIdentity struct {
	PrivateKey string `json:"privateKey"`
	ShortID    string `json:"shortId"`
}

// Reality target/port failures must not stop the existing HY2 admission Agent.
// The upgrade health gate still fails and rolls back if Reality is not ready.
// A later target/network recovery can re-enable the ingress without restarting HY2.
func (a *Agent) realityLoop(ctx context.Context) {
	if a.cfg.Reality == nil {
		return
	}
	for ctx.Err() == nil {
		stop, err := a.startReality(ctx)
		if err != nil {
			a.log.Warn("Reality ingress unavailable; retrying")
		}
		if stop != nil {
			ticker := time.NewTicker(5 * time.Second)
			for a.realityReport() != nil && ctx.Err() == nil {
				select {
				case <-ctx.Done():
				case <-ticker.C:
				}
			}
			ticker.Stop()
			stop()
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// loadRealityIdentity generates once and never rotates on upgrades/restarts.
// The private key stays in the node's private state directory, outside its ledger
// and control database. Malformed existing keys fail closed rather than rotate.
func loadRealityIdentity(statePath string) (realityIdentity, error) {
	var identity realityIdentity
	path := filepath.Join(filepath.Dir(statePath), "reality-identity.json")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || privateConfigMode(info.Mode()) != nil {
			return identity, errors.New("unsafe Reality identity file")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &identity) != nil {
			return identity, errors.New("cannot load Reality identity")
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return identity, err
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return identity, err
	}
	var short [8]byte
	if _, err = rand.Read(short[:]); err != nil {
		return identity, err
	}
	identity = realityIdentity{PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), ShortID: hex.EncodeToString(short[:])}
	data, _ := json.Marshal(identity)
	f, err := os.CreateTemp(filepath.Dir(path), ".reality-identity-*")
	if err != nil {
		return identity, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return identity, err
}

func (a *Agent) startReality(ctx context.Context) (func(), error) {
	if a.cfg.Reality == nil {
		return func() {}, nil
	}
	cfg := a.cfg.Reality
	identity, err := loadRealityIdentity(a.cfg.StatePath)
	if err != nil {
		return nil, err
	}
	private, err := base64.RawURLEncoding.DecodeString(identity.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid Reality private key")
	}
	key, err := ecdh.X25519().NewPrivateKey(private)
	if err != nil {
		return nil, errors.New("invalid Reality private key")
	}
	endpoint := model.RealityEndpoint{Port: cfg.Port, ServerName: cfg.ServerName, ShortID: identity.ShortID, PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())}
	if err = endpoint.Validate(); err != nil {
		return nil, err
	}
	var short [8]byte
	_, _ = hex.Decode(short[:], []byte(endpoint.ShortID))
	dialer := &net.Dialer{Timeout: 5 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(a.cfg.PublicIP)}}
	tlsConfig := &utls.RealityConfig{
		Type: "tcp", Dest: net.JoinHostPort(cfg.ServerName, "443"), DialContext: dialer.DialContext,
		Config:      utls.Config{ServerName: cfg.ServerName, SessionTicketsDisabled: true},
		ServerNames: map[string]bool{cfg.ServerName: true},
		PrivateKey:  private, ShortIds: map[[8]byte]bool{short: true}, MaxTimeDiff: 2 * time.Minute,
	}
	gateway, err := reality.New(reality.Config{TLS: tlsConfig, Resolve: a.resolveRealityCredential, Connect: a.connectRealityBackend})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(a.cfg.PublicIP, strconv.Itoa(cfg.Port)))
	if err != nil {
		return nil, errors.New("Reality TCP port unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := gateway.Serve(ctx, listener); err != nil {
			a.log.Error("Reality ingress stopped")
		}
		a.realityMu.Lock()
		a.realityEndpoint = nil
		a.realityMu.Unlock()
	}()
	stop := func() { cancel(); <-done }
	// A listening socket alone is not a usable Reality endpoint. Authenticate
	// through the target handshake before advertising it or passing upgrade QA.
	probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
	defer probeCancel()
	probe, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", listener.Addr().String())
	if err == nil {
		_, err = reality.Handshake(probeCtx, probe, endpoint)
		_ = probe.Close()
	}
	if err != nil {
		stop()
		return nil, errors.New("Reality startup handshake failed")
	}
	a.realityMu.Lock()
	select {
	case <-done:
		a.realityMu.Unlock()
		cancel()
		return nil, errors.New("Reality ingress stopped during startup")
	default:
	}
	a.realityEndpoint = &endpoint
	a.realityMu.Unlock()
	return stop, nil
}

func (a *Agent) resolveRealityCredential(ctx context.Context, uuid string) (string, error) {
	a.cacheMu.RLock()
	allowed := a.cache.PortalEnabled && a.cache.Retirement == nil
	a.cacheMu.RUnlock()
	if !allowed {
		return "", errors.New("Reality admission unavailable")
	}
	var response struct {
		Password string `json:"password"`
	}
	status, data, err := a.requestAdmission(ctx, "/api/v1/agents/reality-credential", map[string]string{"uuid": uuid})
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &response) != nil {
		return "", errors.New("Reality credential unavailable")
	}
	if len(response.Password) != 43 || model.RealityUUID(response.Password) != uuid {
		return "", errors.New("invalid Reality credential response")
	}
	return response.Password, nil
}

// The loop-local HY2 connection uses the same server certificate validation and
// per-scope password as ordinary clients. No probe or unlimited credential is
// used; all business traffic remains charged by the existing forwarding core.
func (a *Agent) connectRealityBackend(ctx context.Context, password string) (hy.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	client, _, err := hy.NewClient(&hy.Config{
		ServerAddr: &net.UDPAddr{IP: net.ParseIP(a.cfg.PublicIP), Port: a.cfg.Reality.BackendPort}, Auth: password,
		TLSConfig:  hy.TLSConfig{ServerName: a.cfg.Domain},
		QUICConfig: hy.QUICConfig{DisableChromeParrot: true, MaxIdleTimeout: 10 * time.Second, KeepAlivePeriod: 2 * time.Second},
	})
	if err == nil && ctx.Err() != nil {
		_ = client.Close()
		return nil, ctx.Err()
	}
	return client, err
}

func (a *Agent) realityReport() *model.RealityEndpoint {
	a.realityMu.RLock()
	defer a.realityMu.RUnlock()
	if a.realityEndpoint == nil {
		return nil
	}
	copy := *a.realityEndpoint
	return &copy
}
