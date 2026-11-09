package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Only fixed event/reason codes are logged. No URLs, credentials, packet data,
// remote error strings or user browsing destinations enter this file.
func (c *Controller) recoveryLog(event, reason string, attempt int) {
	c.logMu.Lock()
	defer c.logMu.Unlock()
	path := filepath.Join(c.dir, "recovery.log")
	b, _ := json.Marshal(struct {
		At      time.Time `json:"at"`
		Event   string    `json:"event"`
		Reason  string    `json:"reason"`
		Attempt int       `json:"attempt,omitempty"`
	}{time.Now().UTC(), event, reason, attempt})
	b = append(b, '\n')
	if info, err := os.Stat(path); err == nil && info.Size()+int64(len(b)) > 1<<20 {
		_ = os.Remove(path + ".2")
		for i := 1; i >= 0; i-- {
			from := path
			if i > 0 {
				from += "." + strconv.Itoa(i)
			}
			if err := os.Rename(from, path+"."+strconv.Itoa(i+1)); err != nil && !os.IsNotExist(err) {
				return
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(b)
}
