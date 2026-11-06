package model

import "time"

type Metrics struct {
	CPUPercent   float64 `json:"cpuPercent"`
	MemoryUsed   uint64  `json:"memoryUsed"`
	MemoryTotal  uint64  `json:"memoryTotal"`
	DiskUsed     uint64  `json:"diskUsed"`
	DiskTotal    uint64  `json:"diskTotal"`
	UploadRate   float64 `json:"uploadRate"`
	DownloadRate float64 `json:"downloadRate"`
}

type Node struct {
	ID                  string             `json:"id"`
	ServerID            string             `json:"serverId"`
	AddressID           string             `json:"addressId"`
	MachineID           string             `json:"machineId"`
	AuthPort            int                `json:"authPort"`
	StatsPort           int                `json:"statsPort"`
	InstallationVersion int                `json:"installationVersion"`
	CertificateMode     string             `json:"certificateMode"`
	DNSMode             string             `json:"dnsMode"`
	ZoneID              string             `json:"zoneId,omitempty"`
	Managed             bool               `json:"managed"`
	Lifecycle           string             `json:"lifecycle"`
	Revision            int64              `json:"revision"`
	DeletedAt           *time.Time         `json:"deletedAt,omitempty"`
	Capabilities        []string           `json:"capabilities"`
	Retirement          *RetirementStatus  `json:"retirement,omitempty"`
	Certificate         *CertificateStatus `json:"certificate,omitempty"`
	Name                string             `json:"name"`
	SSHHost             string             `json:"sshHost"`
	SSHPort             int                `json:"sshPort"`
	SSHUser             string             `json:"sshUser"`
	PublicIP            string             `json:"publicIp"`
	Domain              string             `json:"domain"`
	Port                int                `json:"port"`
	BandwidthMbps       *float64           `json:"bandwidthMbps"`
	MonthlyQuotaBytes   *int64             `json:"monthlyQuotaBytes"`
	Billing             ServerBilling      `json:"billing"`
	Status              string             `json:"status"`
	Message             string             `json:"message"`
	HostKey             string             `json:"hostKey"`
	SharedHost          bool               `json:"sharedHost"`
	ControlOnNode       bool               `json:"controlOnNode"`
	ACMEViaCaddy        bool               `json:"acmeViaCaddy"`
	CreatedAt           time.Time          `json:"createdAt"`
	LastSeen            *time.Time         `json:"lastSeen"`
	Metrics             Metrics            `json:"metrics"`
	MonthUpload         int64              `json:"monthUpload"`
	MonthDownload       int64              `json:"monthDownload"`
	TotalUpload         int64              `json:"totalUpload"`
	TotalDownload       int64              `json:"totalDownload"`
	OnlineDevices       int                `json:"onlineDevices"`
	Instances           int                `json:"instances"`
	DesiredRevision     int64              `json:"desiredRevision"`
	AppliedRevision     int64              `json:"appliedRevision"`
	CoreHealthy         bool               `json:"coreHealthy"`
}

type Device struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Enabled         bool       `json:"enabled"`
	CreatedAt       time.Time  `json:"createdAt"`
	SubscriptionURL string     `json:"subscriptionUrl,omitempty"`
	MonthUpload     int64      `json:"monthUpload"`
	MonthDownload   int64      `json:"monthDownload"`
	TotalUpload     int64      `json:"totalUpload"`
	TotalDownload   int64      `json:"totalDownload"`
	Online          bool       `json:"online"`
	Instances       int        `json:"instances"`
	PendingSync     bool       `json:"pendingSync"`
	DeletedAt       *time.Time `json:"deletedAt,omitempty"`
	Lifecycle       string     `json:"lifecycle"`
}

type AgentDevice struct {
	ID                 string `json:"id"`
	Password           string `json:"password"`
	Enabled            bool   `json:"enabled"`
	RemovalID          string `json:"removalId,omitempty"`
	RevokeAfterSeconds int    `json:"revokeAfterSeconds,omitempty"`
}

// Deadline is chosen once by the node on first receipt and survives restarts.
type AgentRemovalStatus struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"deviceId"`
	Status   string    `json:"status"`
	Deadline time.Time `json:"deadline"`
}

type AgentDesired struct {
	PortalEnabled bool               `json:"portalEnabled"`
	Revision      int64              `json:"revision"`
	Devices       []AgentDevice      `json:"devices"`
	Retirement    *AgentRetirement   `json:"retirement,omitempty"`
	Certificate   *CertificateBundle `json:"certificate,omitempty"`
}

// Bucket counters are cumulative for a fixed UTC Unix-second interval start.
// Daily buckets start at midnight Asia/Shanghai; repeated reports use MAX,
// never SUM, making retransmission and acknowledgment loss idempotent.
type UsageBucket struct {
	DeviceID  string `json:"deviceId"`
	Start     int64  `json:"start"`
	Upload    int64  `json:"upload"`
	Download  int64  `json:"download"`
	Uncertain bool   `json:"uncertain,omitempty"`
}

type AgentReport struct {
	At               time.Time            `json:"at"`
	Revision         int64                `json:"revision"`
	Metrics          Metrics              `json:"metrics"`
	CoreHealthy      bool                 `json:"coreHealthy"`
	Online           map[string]int       `json:"online"`
	Daily            []UsageBucket        `json:"daily"`
	Minutes          []UsageBucket        `json:"minutes"`
	Hours            []UsageBucket        `json:"hours,omitempty"`
	HourlySince      *time.Time           `json:"hourlySince,omitempty"`
	HourlyThrough    *time.Time           `json:"hourlyThrough,omitempty"`
	HistoryCompacted bool                 `json:"historyCompacted"`
	Removals         []AgentRemovalStatus `json:"removals,omitempty"`
	Warnings         []string             `json:"warnings,omitempty"`
	Capabilities     []string             `json:"capabilities,omitempty"`
	Retirement       *RetirementStatus    `json:"retirement,omitempty"`
	Certificate      *CertificateStatus   `json:"certificate,omitempty"`
}

type AgentConfig struct {
	ControlURL      string `json:"controlUrl"`
	NodeID          string `json:"nodeId"`
	Token           string `json:"token"`
	Listen          string `json:"listen"`
	StatePath       string `json:"statePath"`
	StatsURL        string `json:"statsUrl"`
	StatsSecret     string `json:"statsSecret"`
	HysteriaService string `json:"hysteriaService"`
	NodeDir         string `json:"nodeDir"`
	Managed         bool   `json:"managed"`
	Domain          string `json:"domain"`
	PublicIP        string `json:"publicIp"`
	AgentService    string `json:"agentService"`
	CertificateMode string `json:"certificateMode"`
	ControllerID    string `json:"controllerId"`
}

type TaskEvent struct {
	ID        int64     `json:"id"`
	NodeID    string    `json:"nodeId"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type TrafficPoint struct {
	At       int64 `json:"at"`
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}
