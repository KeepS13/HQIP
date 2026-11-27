//go:build !windows

package client

import "crypto/tls"

func accountTLSFallback(_, _ string, _ *tls.Config) accountTLSDial { return nil }
