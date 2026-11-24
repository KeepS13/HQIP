extends SceneTree
## Slow native replies are deliberate: the selected pair must be visible in
## the click handler, without waiting for a frame, disk, polling or a probe.
const Main = preload("res://ui/main.gd")
var failures: Array[String] = []
var ui: Control

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
	root.size = Vector2i(1040, 820)
	ui = Main.new()
	ui.preview = true
	root.add_child(ui)
	ui.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	var fixture: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	fixture.merge({"runState":"disconnected", "connectionIntent":false, "actualNodeId":"", "actualSubscriptionId":"", "nodeTests":{"pending":false, "results":[]}}, true)
	var a: Dictionary = fixture.subscriptions[0]
	var b := a.merged({"id":"plan-b", "templateName":"套餐 B"}, true)
	var c := a.merged({"id":"plan-c", "templateName":"套餐 C"}, true)
	fixture.subscriptions = [a, b, c]
	fixture.nodeChoices.append({"nodeId":"node-b", "subscriptionId":b.id, "name":"B 节点", "ready":true})
	fixture.nodeChoices.append({"nodeId":"node-c", "subscriptionId":c.id, "name":"C 节点", "ready":true})
	ui.render(fixture)
	var pipe := DelayedBridge.new()
	ui.add_child(pipe)
	ui.bridge = pipe
	ui.preview = false # Fake transport only; _ready already skipped native startup.
	var choice_b := {"subscriptionId":b.id, "nodeId":"node-b"}
	var choice_c := {"subscriptionId":c.id, "nodeId":"node-c"}
	ui.dashboard.choose_node.emit()
	var start := Time.get_ticks_usec()
	ui.picker.grid.get_child(2).pressed.emit()
	check(not ui.picker.visible, "Node picker stayed open while saving")
	check(ui.dashboard.node_name.text == "B 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 B"), "Node and plan must change atomically inside the click handler")
	check(ui.dashboard.summary_title.text == "套餐 B", "Subscription summary lagged behind the chosen node")
	check(ui.dashboard.connection.status.text == "未连接" and ui.dashboard.connection.power.disabled, "Local save pretended to connect or allowed connecting the previous node")
	print("Local selection render usec: ", Time.get_ticks_usec() - start)
	ui.render(fixture) # An in-flight old poll must not erase the latest UI choice.
	check(ui.dashboard.node_name.text == "B 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 B"), "Old snapshot replaced immediate selection")
	ui._command("node", choice_c)
	check(ui.dashboard.node_name.text == "C 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 C"), "Rapid second choice was ignored")
	check(pipe.sent.size() == 1, "Unconfirmed saves were sent concurrently")
	var confirmed_b := fixture.merged({"desiredSubscriptionId":b.id, "desiredNodeId":"node-b", "subscription":b}, true)
	ui._reply("node", confirmed_b, "")
	check(ui.dashboard.node_name.text == "C 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 C"), "Earlier acknowledgement flashed the earlier node")
	check(pipe.sent.size() == 2 and pipe.sent.back() == ["node", choice_c], "Latest queued choice was not saved")
	var confirmed_c := fixture.merged({"desiredSubscriptionId":c.id, "desiredNodeId":"node-c", "subscription":c}, true)
	ui._reply("node", confirmed_c, "")
	check(not ui.dashboard.connection.power.disabled, "Confirmed choice did not unlock Connect")
	ui._command("node", choice_b)
	ui._reply("node", null, "模拟本地保存失败")
	check(ui.dashboard.node_name.text == "C 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 C"), "Failed save did not roll back both labels")
	check(ui.action_error == "模拟本地保存失败", "Persistence error was hidden")
	var before := pipe.sent.size()
	ui._command("node", choice_c)
	check(pipe.sent.size() == before, "Re-selecting the confirmed node performed redundant work")
	# A→B→C→A while B saves keeps only the final queued pair. An obsolete
	# failure must not erase the newer choice or display its error as current.
	ui._command("node", choice_b)
	ui._command("node", {"subscriptionId":a.id, "nodeId":"node-a"})
	ui._command("node", choice_c)
	ui._reply("node", null, "已被后续选择替代的失败")
	check(ui.dashboard.node_name.text == "C 节点" and ui.action_error == "", "Obsolete save failure affected latest choice")
	check(pipe.sent.size() == before + 1, "Returning to confirmed choice caused an unnecessary queued write")
	# A native snapshot may still expose its authorized package during a live
	# switch. The top selector must identify the desired pair consistently.
	ui.render(confirmed_b.merged({"subscription":a}, true))
	check(ui.dashboard.node_name.text == "B 节点" and ui.dashboard.node_subscription.text.begins_with("套餐 B"), "Selector mixed desired node with authorized package")
	check(pipe.sent.all(func(item): return item[0] == "node"), "Local selection triggered a probe, refresh or connection")
	ui._command("node", choice_c)
	ui.render(fixture.merged({"loggedIn":false}, true))
	ui._reply("node", confirmed_c, "")
	check(not ui.state.loggedIn and not ui.local_selection.busy(), "Late choice acknowledgement resurrected a signed-out account")
	ui.preview = true
	print("PASS instant local selection" if failures.is_empty() else "FAIL instant local selection: %d" % failures.size())
	quit(0 if failures.is_empty() else 1)
