// hqip-client-check exercises the actual desktop controller and pinned core.
// By default it does not take over the computer's system proxy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KeepS13/HQIP/internal/client"
	"github.com/KeepS13/HQIP/internal/winclient"
)

type isolatedPlatform struct{ *winclient.Adapter }

func (p isolatedPlatform) EnableProxy(int) error { return nil }
func (p isolatedPlatform) RestoreProxy() error   { return nil }

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	server := flag.String("server", client.DefaultServerURL, "account API origin")
	subscription := flag.String("subscription", "", "subscription instance to use on this test installation")
	corePath := flag.String("core", "dist/desktop/sing-box.exe", "fixed sing-box executable")
	state := flag.String("state", ".local/client-check", "isolated state directory")
	out := flag.String("output", ".local/client-check-status.json", "sanitized status JSON")
	hold := flag.Duration("hold", 20*time.Second, "observation duration")
	tun := flag.Bool("tun", false, "verify the elevated TUN service")
	system := flag.Bool("system-proxy", false, "verify actual system proxy takeover and restoration")
	connect := flag.Bool("connect", true, "connect after login")
	flag.Parse()
	dir, e := filepath.Abs(*state)
	if e != nil {
		return e
	}
	binary, e := filepath.Abs(*corePath)
	if e != nil {
		return e
	}
	native, e := winclient.New(dir, binary)
	if e != nil {
		return e
	}
	var p client.Platform = isolatedPlatform{native}
	if *system || *tun {
		p = native
	}
	engine := &client.Core{Path: binary, Dir: filepath.Join(dir, "runtime"), Platform: p}
	c, e := client.New(dir, p, engine)
	if e != nil {
		return e
	}
	defer c.Close()
	if c.View().LoggedIn {
		_ = c.Logout()
	}
	if e = c.LoginBrowser(*server, winclient.OpenBrowser); e != nil {
		return e
	}
	defer c.Logout()
	if *subscription != "" {
		if e = c.SelectSubscription(*subscription); e != nil {
			return e
		}
		until := time.Now().Add(150 * time.Second)
		for c.View().SelectionPending && time.Now().Before(until) {
			time.Sleep(100 * time.Millisecond)
		}
		if c.View().SelectionPending || c.View().DesiredSubscriptionID != *subscription || c.View().LastError != "" {
			return errors.New("subscription selection did not complete")
		}
	}
	if e = c.SetMode("rule", *tun); e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	if *connect {
		if e = c.Connect(); e != nil {
			return e
		}
	}
	deadline := time.Now().Add(*hold)
	connected := false
	var last string
	for time.Now().Before(deadline) {
		v := c.View()
		if v.RunState == "connected" {
			connected = true
		}
		data, _ := json.MarshalIndent(v, "", "  ")
		tmp := *out + ".new"
		if e = os.WriteFile(tmp, data, 0600); e == nil {
			_ = os.Rename(tmp, *out)
		}
		label := v.RunState + " " + v.ActualNodeID + " " + v.LastError
		if label != last {
			fmt.Println(label)
			last = label
		}
		time.Sleep(time.Second)
	}
	_ = c.Disconnect()
	if *connect && !connected {
		return errors.New("client never established a verified proxy connection")
	}
	return nil
}
