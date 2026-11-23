package server

import (
	"context"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// HY2 adapts its QUIC connection lifetime to the shared policy module.
type AdmissionConfig struct {
	URL    string `mapstructure:"url"`
	Secret string `mapstructure:"secret"`
}
type LeaseGrant = nodepolicy.LeaseGrant
type Admission struct{ *nodepolicy.Admission }
type connectionLease struct{ *nodepolicy.Lease }

func NewAdmission(config AdmissionConfig) (*Admission, error) {
	admission, err := nodepolicy.NewAdmission(nodepolicy.AdmissionConfig{URL: config.URL, Secret: config.Secret})
	if err != nil {
		return nil, err
	}
	return &Admission{admission}, nil
}
func randomConnectionID() string { return nodepolicy.NewConnectionID() }
func (a *Admission) open(ctx context.Context, id, password, peerIP string, closeConnection func()) (*connectionLease, string, error) {
	lease, account, err := a.Open(ctx, id, password, peerIP, closeConnection)
	if err != nil {
		return nil, "", err
	}
	return &connectionLease{lease}, account, nil
}
func (l *connectionLease) finish() {
	if l != nil {
		l.Finish()
	}
}
