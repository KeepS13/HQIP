extends SceneTree
## Sequence reflects capture, not request order. A slow earlier command may
## capture the latest view; malformed legacy acknowledgements must be explicit.
const Bridge = preload("res://runtime/bridge.gd")
var failures: Array[String] = []

class PipeFixture extends RefCounted:
	var sent: Array = []
	func submit(raw: String) -> void: sent.append(JSON.parse_string(raw))

func _initialize() -> void:
	var bridge := Bridge.new()
	var pipe := PipeFixture.new()
	bridge._plugin = pipe
	var received := []
	bridge.replied.connect(func(method, result, error): received.append([method, result, error]))
	bridge.call_command("node") # 1: runs slowly before capture.
	bridge.call_command("state") # 2: captures first.
	bridge._receive(JSON.stringify({"id":2, "result":{"snapshotSequence":1, "desiredNodeId":"a"}}))
	bridge._receive(JSON.stringify({"id":1, "result":{"snapshotSequence":2, "desiredNodeId":"b"}}))
	check(received.size() == 2 and received.back()[1].desiredNodeId == "b", "Newer capture from an earlier command was lost")
	bridge.call_command("state") # 3: a larger request ID is not a freshness proof.
	bridge._receive(JSON.stringify({"id":3, "result":{"snapshotSequence":1, "desiredNodeId":"a"}}))
	check(received.size() == 2, "Old stamped state bypassed capture ordering")
	bridge.call_command("node") # 4: old 4.x runtimes returned booleans.
	bridge._receive(JSON.stringify({"id":4, "result":true}))
	check(received.size() == 3 and received.back()[1] == null and not received.back()[2].is_empty(), "Legacy boolean acknowledgement silently cleared the chosen node")
	check(pipe.sent.size() == 4, "Reply handling sent extra requests")
	bridge.free()
	print("PASS snapshot compatibility" if failures.is_empty() else "FAIL snapshot compatibility")
	quit(0 if failures.is_empty() else 1)

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)
