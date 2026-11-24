package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

var errChangesUnsupported = errors.New("account change stream unavailable")

// Only token rotation owns authMu. A stream never holds it while reading or
// calling Sync, so Connect/Disconnect/Logout cannot queue behind a heartbeat.
func (a *API) changeStreamToken(ctx context.Context, rejected string) (string, error) {
	a.authMu.Lock()
	defer a.authMu.Unlock()
	tokens := a.Tokens()
	if tokens.RefreshToken == "" {
		return "", ErrUnauthorized
	}
	if time.Until(tokens.AccessExpiresAt) < time.Minute || rejected != "" && tokens.AccessToken == rejected {
		if err := a.refresh(ctx); err != nil {
			return "", err
		}
	}
	return a.Tokens().AccessToken, nil
}

// The stream is optional and carries no credentials/configuration. A stalled
// read is cancelled after 45s (three heartbeats); parent cancellation interrupts
// it immediately. Lines are bounded at 4KiB and redirects stay forbidden.
func (a *API) watchChanges(ctx context.Context, notify func(string) error) error {
	rejected := ""
	for attempt := 0; attempt < 2; attempt++ {
		token, err := a.changeStreamToken(ctx, rejected)
		if err != nil {
			return err
		}
		err = a.readChanges(ctx, token, notify)
		if !errors.Is(err, ErrUnauthorized) {
			return err
		}
		rejected = token
	}
	return ErrUnauthorized
}

func (a *API) readChanges(parent context.Context, token string, notify func(string) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	timeout := time.AfterFunc(45*time.Second, cancel)
	defer timeout.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/api/v3/client/changes", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/x-ndjson")
	client := *a.http // Reuse the direct transport; ordinary requests keep their 25s budget.
	client.Timeout = 0
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented {
		return errChangesUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return temporary(errors.New("账户变更通知暂不可用"))
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/x-ndjson") {
		return errChangesUnsupported
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 512), 4096)
	for scanner.Scan() {
		var message struct {
			ChangeToken string `json:"changeToken"`
			Reauth      bool   `json:"reauth"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return errors.New("账户变更通知格式无效")
		}
		if message.Reauth {
			return ErrUnauthorized
		}
		if len(message.ChangeToken) != 64 {
			return errors.New("账户变更标识无效")
		}
		// Account sync has its own bounded requests. Do not mistake time spent
		// processing a received change for a stalled stream read.
		timeout.Stop()
		if err = notify(message.ChangeToken); err != nil {
			return err
		}
		timeout.Reset(45 * time.Second)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return temporary(errors.New("账户变更通知连接已中断"))
}
