package billing

import (
	"errors"
	"math"
	"time"
)

const Capability = "traffic-billing-v1"
const MaxRawBytes int64 = math.MaxInt64 / 1000

type Policy struct {
	Version    int64  `json:"version"`
	Multiplier int64  `json:"multiplier"`
	Protocol   string `json:"protocol"`
}

type Grant struct {
	Policy
	BudgetUnits    int64     `json:"budgetUnits"`
	ConsumedUnits  int64     `json:"consumedUnits"`
	EmergencyBytes int64     `json:"emergencyBytes,omitempty"`
	Acknowledged   []Segment `json:"acknowledged,omitempty"`
}

type Segment struct {
	Version           int64     `json:"version"`
	Upload            int64     `json:"upload"`
	Download          int64     `json:"download"`
	EmergencyUpload   int64     `json:"emergencyUpload,omitempty"`
	EmergencyDownload int64     `json:"emergencyDownload,omitempty"`
	AppliedAt         time.Time `json:"appliedAt"`
	ValidUntil        time.Time `json:"validUntil,omitempty"`
}

func (segment Segment) Equal(other Segment) bool {
	return segment.Version == other.Version && segment.Upload == other.Upload && segment.Download == other.Download && segment.EmergencyUpload == other.EmergencyUpload && segment.EmergencyDownload == other.EmergencyDownload && segment.AppliedAt.Equal(other.AppliedAt) && segment.ValidUntil.Equal(other.ValidUntil)
}

func (policy Policy) Valid() bool {
	return policy.Version > 0 && policy.Multiplier >= 1 && policy.Multiplier <= 1000 && (policy.Protocol == "hysteria2" || policy.Protocol == "vless")
}

func Units(raw, multiplier int64) (int64, error) {
	if raw < 0 || multiplier < 1 || multiplier > 1000 || raw > math.MaxInt64/multiplier {
		return 0, errors.New("invalid or overflowing traffic charge")
	}
	return raw * multiplier, nil
}

func Add(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errors.New("traffic counter overflow")
	}
	return left + right, nil
}

func Ceiling(units int64) int64 {
	return units/100 + min(int64(1), units%100)
}

func (segment Segment) Validate() error {
	if segment.Version <= 0 || segment.Upload < 0 || segment.Download < 0 || segment.EmergencyUpload < 0 || segment.EmergencyDownload < 0 || segment.EmergencyUpload > segment.Upload || segment.EmergencyDownload > segment.Download || segment.AppliedAt.IsZero() {
		return errors.New("invalid billing segment")
	}
	_, err := Add(segment.Upload, segment.Download)
	return err
}
