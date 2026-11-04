package model

// NotificationCondition is a safe, rendered observation, never a raw log. Key
// identifies one resource/condition/period and deliberately excludes Stage.
type NotificationCondition struct {
	Key             string `json:"key"`
	Category        string `json:"category"`
	Kind            string `json:"kind"`
	ResourceID      string `json:"resourceId"`
	Title           string `json:"title"`
	Message         string `json:"message"`
	DetailPath      string `json:"detailPath"`
	Level           string `json:"level"`
	Stage           int    `json:"stage"`
	Continuous      bool   `json:"continuous"`
	DelaySeconds    int    `json:"delaySeconds"`
	RecoverySeconds int    `json:"recoverySeconds"`
	Unknown         bool   `json:"unknown,omitempty"`
	Removed         bool   `json:"removed,omitempty"`
}
