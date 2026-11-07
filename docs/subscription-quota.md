# Subscription tail balances

New connections require at least **16 KiB (16,384 bytes)** of unreserved quota.
This is an admission policy based on the HQIP Hysteria runtime's maximum payload
accounting chunk, not a protocol minimum or a promise that every application can
complete a request with 16 KiB. A positive balance such as 478 bytes could
previously admit a connection, then close it on its first larger payload while
leaving the subscription marked active.

For finite subscriptions, the server reports `exhausted` when the actual balance
`quotaBytes - usedBytes` is below this floor and no unexpired, unrevoked, open
lease still holds unused authorized bytes. The existing priority remains:
revoked, disabled, expired, then exhausted. All portals and clients consume this
same server status. Exact quota, used, reserved, and remaining byte values remain
available for display and audit; the status never writes off or consumes a tail.

Reservations are not usage. `reservedBytes` is subtracted only when granting a
new lease, to prevent oversubscription. A subscription with enough total balance
can remain active while all of its balance is temporarily reserved. Admission
rejects an additional connection if the unreserved balance is below 16 KiB.

An existing live lease can continue using its authorized tail below the floor.
Its replay and renewal remain valid until it is closed, revoked, expired, or
actually has no budget left. Final usage settles normally; unused reservation
is released, and a sub-floor balance with no remaining live authorization then
reports `exhausted`. Unlimited subscriptions are exempt from this quota policy.
