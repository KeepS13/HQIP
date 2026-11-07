package model

// MinimumSubscriptionLeaseBytes is the admission floor for a new connection.
// It matches one maximum payload accounting chunk in the HQIP Hysteria runtime
// (16 KiB). This is a usability policy, not a protocol minimum: an existing live
// lease may continue spending a smaller authorized balance. Counters and the
// actual remaining quota are never rounded or charged to enforce this floor.
const MinimumSubscriptionLeaseBytes int64 = 16 << 10
