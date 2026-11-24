extends RefCounted
## Presentation-only draft from the authenticated cached catalog. Native Go
## still validates and atomically persists each choice. Exactly one save is in
## flight; rapid clicks replace the queued target instead of losing clicks or
## allowing out-of-order writes. No network/probe/connection is initiated here.
const S = preload("res://ui/style.gd")
var wanted: Dictionary = {}
var inflight: Dictionary = {}

func busy() -> bool:
	return not wanted.is_empty() or not inflight.is_empty()

func clear() -> void:
	wanted.clear()
	inflight.clear()

func stage(snapshot: Dictionary, params: Dictionary) -> bool:
	if not snapshot.get("loggedIn", false):
		return false
	var sub_id: String = str(params.get("subscriptionId", ""))
	var node_id: String = str(params.get("nodeId", ""))
	var available := false
	for sub in snapshot.get("subscriptions", []):
		if sub.id == sub_id and S.valid_subscription(sub): available = true
	if not available: return false
	for node in snapshot.get("nodeChoices", []):
		if node.subscriptionId == sub_id and node.nodeId == node_id and node.get("ready", false):
			wanted = {"subscriptionId":sub_id, "nodeId":node_id}
			return true
	return false

func take_next(snapshot: Dictionary) -> Dictionary:
	if not inflight.is_empty() or wanted.is_empty(): return {}
	if wanted.subscriptionId == snapshot.get("desiredSubscriptionId", "") and wanted.nodeId == snapshot.get("desiredNodeId", ""):
		wanted.clear()
		return {} # Selecting the confirmed pair again is a local no-op.
	inflight = wanted.duplicate()
	return inflight.duplicate()

func finish() -> bool:
	var superseded := wanted != inflight
	if not superseded: wanted.clear()
	inflight.clear()
	return superseded

func present(snapshot: Dictionary) -> Dictionary:
	if wanted.is_empty(): return snapshot
	var shown := snapshot.duplicate() # Own only fields being overlaid; never mutate cached catalog entries.
	shown.desiredSubscriptionId = wanted.subscriptionId
	shown.desiredNodeId = wanted.nodeId
	shown.subscription = null
	shown.nodes = []
	shown.defaultNodeId = ""
	for sub in snapshot.get("subscriptions", []):
		if sub.id == wanted.subscriptionId:
			shown.subscription = sub
			shown.defaultNodeId = sub.get("defaultNodeId", "")
	for node in snapshot.get("nodeChoices", []):
		if node.subscriptionId == wanted.subscriptionId:
			shown.nodes.append({"id":node.nodeId, "name":node.name, "ready":node.ready})
	return shown
