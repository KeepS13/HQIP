package model

import "time"

type Server struct {
	HostNetwork         *HostNetworkUsage `json:"hostNetwork,omitempty"`
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	SSHHost             string            `json:"sshHost"`
	SSHPort             int               `json:"sshPort"`
	SSHUser             string            `json:"sshUser"`
	HostKey             string            `json:"hostKey"`
	MachineID           string            `json:"machineId"`
	Platform            string            `json:"platform"`
	Architecture        string            `json:"architecture"`
	BandwidthMbps       *float64          `json:"bandwidthMbps"`
	MonthlyQuotaBytes   *int64            `json:"monthlyQuotaBytes"`
	Billing             ServerBilling     `json:"billing"`
	Revision            int64             `json:"revision"`
	CreatedAt           time.Time         `json:"createdAt"`
	LastSeen            *time.Time        `json:"lastSeen,omitempty"`
	Metrics             Metrics           `json:"metrics"`
	Addresses           []ServerAddress   `json:"addresses"`
	NodeCount           int               `json:"nodeCount"`
	IPCount             int               `json:"ipCount"`
	MonthUpload         int64             `json:"monthUpload"`
	MonthDownload       int64             `json:"monthDownload"`
	TotalUpload         int64             `json:"totalUpload"`
	TotalDownload       int64             `json:"totalDownload"`
	ExpectedNodes       int               `json:"expectedNodes"`
	ReportingNodes      int               `json:"reportingNodes"`
	RatesStatus         string            `json:"ratesStatus"`
	MetricsFresh        bool              `json:"metricsFresh"`
	MetricsSourceNodeID string            `json:"metricsSourceNodeId"`
	MetricsReportedAt   *time.Time        `json:"metricsReportedAt"`
}

// UsageTotals are device proxy bytes from the durable ledger, excluding probes.
type UsageTotals struct {
	MonthUpload   int64 `json:"monthUpload"`
	MonthDownload int64 `json:"monthDownload"`
	TotalUpload   int64 `json:"totalUpload"`
	TotalDownload int64 `json:"totalDownload"`
}

type ServerAddress struct {
	ID        string    `json:"id"`
	ServerID  string    `json:"serverId"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
	NodeIDs   []string  `json:"nodeIds"`
}

type NodeReplacement struct {
	DefaultNodeID    string `json:"defaultNodeId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}
type NodeDeleteOptions struct {
	ExpectedRevision int64                      `json:"expectedRevision"`
	Replacements     map[string]NodeReplacement `json:"replacements"`
}

type AgentRetirement struct {
	ID       string `json:"id"`
	Finalize bool   `json:"finalize"`
}
type RetirementStatus struct {
	ID      string `json:"id"`
	Stopped bool   `json:"stopped"`
	Final   bool   `json:"final"`
}
type CertificateBundle struct {
	Version        int64     `json:"version"`
	Domain         string    `json:"domain"`
	CertificatePEM string    `json:"certificatePem"`
	PrivateKeyPEM  string    `json:"privateKeyPem"`
	NotBefore      time.Time `json:"notBefore"`
	NotAfter       time.Time `json:"notAfter"`
}
type CertificateStatus struct {
	Version  int64     `json:"version"`
	NotAfter time.Time `json:"notAfter"`
	Error    string    `json:"error,omitempty"`
}
