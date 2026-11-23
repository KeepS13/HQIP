package server

import (
	"context"
	"github.com/KeepS13/HQIP/runtime/nodepolicy"
)

// HY2 authenticates QUIC once; forwarding leases belong to active business
// sockets, so client keepalives alone never reserve an online slot.
type AdmissionConfig struct {
	URL    string `mapstructure:"url"`
	Secret string `mapstructure:"secret"`
}
type LeaseGrant = nodepolicy.LeaseGrant
type Admission struct{ *nodepolicy.Admission }

func NewAdmission(config AdmissionConfig) (*Admission, error) {
	admission, err := nodepolicy.NewAdmission(nodepolicy.AdmissionConfig{URL: config.URL, Secret: config.Secret})
	if err != nil {
		return nil, err
	}
	return &Admission{admission}, nil
}
func randomConnectionID() string { return nodepolicy.NewConnectionID() }
func (a *Admission) open(ctx context.Context, id, password, peerIP string, closeConnection func()) (*connectionLease, string, error) {
	account, err := a.Check(ctx, id, password, peerIP)
	if err != nil {
		return nil, "", err
	}
	return &connectionLease{admission: a, ctx: ctx, password: password, peerIP: peerIP, account: account, closeConnection: closeConnection}, account, nil
}
