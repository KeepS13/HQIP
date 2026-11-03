package model

import "time"

type Server struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	SSHHost      string          `json:"sshHost"`
	SSHPort      int             `json:"sshPort"`
	SSHUser      string          `json:"sshUser"`
	HostKey      string          `json:"hostKey"`
	MachineID    string          `json:"machineId"`
	Platform     string          `json:"platform"`
	Architecture string          `json:"architecture"`
	Revision     int64           `json:"revision"`
	CreatedAt    time.Time       `json:"createdAt"`
	LastSeen     *time.Time      `json:"lastSeen,omitempty"`
	Metrics      Metrics         `json:"metrics"`
	Addresses    []ServerAddress `json:"addresses"`
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
