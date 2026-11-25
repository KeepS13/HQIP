package model

import "time"

// HostNetworkSample contains operating-system NIC counters, never subscription
// bytes. BootID and interface index identify counters shared by all local agents.
// RX/TX are host directions; both proxy legs, overhead and system traffic count.
type HostNetworkSample struct {
	BootID     string                 `json:"bootId"`
	BootAt     time.Time              `json:"bootAt"`
	At         time.Time              `json:"at"`
	Interfaces []HostNetworkInterface `json:"interfaces"`
	Excluded   []string               `json:"excluded,omitempty"`
}

type HostNetworkInterface struct {
	Name    string `json:"name"`
	Index   int    `json:"index"`
	Receive int64  `json:"receive"`
	Send    int64  `json:"send"`
}

// HostNetworkUsage is administrator-only host scope, shared across node cards.
// Totals include counters already present at first observation, not fabricated
// monthly history. Historical gaps and unknown process attribution stay explicit.
type HostNetworkUsage struct {
	ServerID      string            `json:"serverId"`
	Receive       int64             `json:"receive"`
	Send          int64             `json:"send"`
	FirstObserved time.Time         `json:"firstObserved"`
	Sample        HostNetworkSample `json:"sample"`
	ReceiveRate   *float64          `json:"receiveRate"`
	SendRate      *float64          `json:"sendRate"`
	Discontinuity bool              `json:"discontinuity"`
	Fresh         bool              `json:"fresh"`
}
