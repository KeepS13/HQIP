extends SceneTree
## Render native current-connection measurements without emitting probe commands.
const Main = preload("res://ui/main.gd")
var failures: Array[String] = []

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	var ui := Main.new()
	ui.preview = true
	root.add_child(ui)
	var state: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	state.nodeTests = {"results":[]}
	state.metrics.delayPending = true
	state.metrics.delayMs = null
	ui.render(state)
	check(ui.dashboard.node_latency.text == "检测中…", "Connected measurement progress missing")
	state.metrics.delayPending = false
	state.metrics.delayMs = 38
	ui.render(state)
	check(ui.dashboard.node_latency.text == "38 ms" and ui.dashboard.connection.caption.text.contains("38 ms"), "Actual connection delay not shown")
	state.desiredNodeId = "node-b"
	ui.render(state)
	check(ui.dashboard.node_latency.text == "", "A's delay was attributed to newly selected B")
	state.runState = "connecting"
	state.selectionPending = true
	ui.render(state)
	check(not ui.dashboard.connection.caption.text.contains("38 ms") and not ui.dashboard.node_button.disabled, "Switch shows stale delay or prevents another choice")
	state.runState = "connected"
	state.selectionPending = false
	state.actualNodeId = "node-b"
	state.metrics.delayMs = 52
	ui.render(state)
	check(ui.dashboard.node_latency.text == "52 ms", "New connected node did not receive its own delay")
	state.runState = "disconnected"
	state.connectionIntent = false
	state.actualNodeId = ""
	ui.render(state)
	check(ui.dashboard.node_latency.text == "" and not ui.dashboard.connection.caption.text.contains("52 ms"), "Disconnected UI still displays connection delay")
	check(ui.bridge == null, "Render started a native runtime")
	ui.free()
	print("PASS current connection delay" if failures.is_empty() else "FAIL current connection delay")
	quit(0 if failures.is_empty() else 1)
