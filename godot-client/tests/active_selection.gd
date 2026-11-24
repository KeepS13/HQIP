extends SceneTree
## Simulated slow native persistence, including a connection already in flight.
## A local click must immediately change the selector, never the actual card.
const Main = preload("res://ui/main.gd")
var failures: Array[String] = []

class DelayedBridge extends Node:
	var sent: Array = []
	func call_command(method: String, params: Dictionary = {}) -> bool:
		sent.append([method, params.duplicate(true)])
		return true

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	var fixture: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	var active: Dictionary = fixture.subscriptions[0]
	var other := active.merged({"id":"next-plan", "templateName":"下一份套餐"}, true)
	fixture.subscriptions = [active, other]
	fixture.nodeChoices.append({"nodeId":"next-node", "subscriptionId":other.id, "name":"下一节点", "ready":true})
	fixture.actualNodeId = "node-a"
	fixture.actualSubscriptionId = active.id
	for running in ["connected", "connecting", "recovering"]:
		var ui := Main.new()
		ui.preview = true
		root.add_child(ui)
		ui.render(fixture.merged({"runState":running, "connectionIntent":true, "selectionPending":running == "connecting"}, true))
		var bridge := DelayedBridge.new()
		ui.add_child(bridge)
		ui.bridge = bridge
		ui.preview = false
		var before: String = ui.dashboard.connection.detail.text
		check(not ui.dashboard.node_button.disabled, running + " locked the node selector")
		ui._command("node", {"subscriptionId":other.id, "nodeId":"next-node"})
		check(ui.dashboard.node_name.text == "下一节点" and ui.dashboard.summary_title.text == "下一份套餐", running + " delayed selection feedback")
		check(ui.dashboard.connection.detail.text == before and ui.state.actualNodeId == "node-a", running + " relabeled the actual connection")
		check(not ui.dashboard.connection.power.disabled, running + " blocked Disconnect while saving a choice")
		check(bridge.sent.size() == 1 and bridge.sent[0][0] == "node", running + " issued an extra command")
		ui.preview = true
		ui.free()
	print("PASS active local selection" if failures.is_empty() else "FAIL active local selection")
	quit(0 if failures.is_empty() else 1)
