package nodepolicy

func (lease *connectionLease) billingAllows(raw int64) bool {
	grant := lease.grant.Billing
	if grant == nil {
		return true
	}
	normal := max(int64(0), grant.BudgetUnits-lease.billingNormal) / grant.Multiplier
	return raw <= normal+max(int64(0), grant.EmergencyBytes-lease.billingEmergency)
}

func (lease *connectionLease) chargeBilling(upload bool, raw int64) {
	grant := lease.grant.Billing
	if grant == nil {
		return
	}
	normal := min(raw, max(int64(0), grant.BudgetUnits-lease.billingNormal)/grant.Multiplier)
	emergency := raw - normal
	lease.billingNormal += normal * grant.Multiplier
	lease.billingEmergency += emergency
	segment := &lease.billingSegments[len(lease.billingSegments)-1]
	if upload {
		segment.Upload += raw
		segment.EmergencyUpload += emergency
	} else {
		segment.Download += raw
		segment.EmergencyDownload += emergency
	}
}
