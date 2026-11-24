extends SceneTree
## Exercise the real command receiver and presentation shell together. A node
## snapshot can be captured before Disconnect but arrive after its state read.
const Main = preload("res://ui/main.gd")
const Bridge = preload("res://runtime/bridge.gd")
var failures: Array[String] = []

class PipeFixture extends RefCounted:
	var sent: Array = []
	func submit(raw: String) -> void: sent.append(JSON.parse_string(raw))

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	var fixture: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	fixture.merge({"connectionIntent":true, "actualNodeId":"node-a", "actualSubscriptionId":fixture.desiredSubscriptionId}, true)
	var ui := Main.new()
	ui.preview = true
	root.add_child(ui)
	ui.render(fixture)
	var bridge := Bridge.new()
	var pipe := PipeFixture.new()
	bridge._plugin = pipe
	ui.add_child(bridge)
	bridge.set_process(false)
	bridge.replied.connect(ui._reply)
	ui.bridge = bridge
	ui.preview = false
	ui._command("node", {"subscriptionId":fixture.desiredSubscriptionId, "nodeId":"node-b"}) # 1
	var before_disconnect := fixture.merged({"snapshotSequence":1, "desiredNodeId":"node-b", "metrics":{"delayPending":true}}, true)
	ui._command("disconnect") # 2
	bridge._receive(JSON.stringify({"id":2, "result":true})) # Schedules state 3.
	var idle := before_disconnect.merged({"snapshotSequence":2, "runState":"disconnected", "connectionIntent":false, "actualNodeId":"", "actualSubscriptionId":"", "metrics":{}, "nodeTests":{"pending":false, "results":[]}}, true)
	bridge._receive(JSON.stringify({"id":3, "result":idle}))
	bridge._receive(JSON.stringify({"id":1, "result":before_disconnect}))
	check(ui.state.runState == "disconnected" and not ui.state.connectionIntent, "Late node acknowledgement resurrected the old connection")
	check(not ui.state.metrics.get("delayPending", false) and ui.dashboard.node_latency.text == "", "Late acknowledgement displayed automatic testing while disconnected")
	check(ui.state.desiredNodeId == "node-b" and not ui.local_selection.busy(), "Discarding an old snapshot lost the persisted choice or left its save pending")
	check(pipe.sent.size() == 3, "Retiring an old acknowledgement issued extra requests")
	# Polling stays passive and must not restart selection, connection or tests.
	for index in range(20):
		bridge.call_command("state")
		bridge._receive(JSON.stringify({"id":4 + index, "result":idle.merged({"snapshotSequence":3 + index}, true)}))
	check(pipe.sent.slice(3).all(func(request): return request.method == "state"), "Idle state handling sent a mutation or probe")
	ui.preview = true
	ui.free()
	print("PASS snapshot order" if failures.is_empty() else "FAIL snapshot order")
	quit(0 if failures.is_empty() else 1)
