extends SceneTree
## Offline UI contract: informative cards, scoped commands and responsive
## content bounds. No bridge, browser, account request or system proxy exists.
const Main = preload("res://ui/main.gd")
var ui: Control
var failures: Array[String] = []
var commands: Array = []

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
	var state: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	state.merge({"runState":"disconnected", "connectionIntent":false, "actualNodeId":"", "actualSubscriptionId":""}, true)
	ui.render(state)
	ui.dashboard.command.connect(func(method, params): commands.append([method, params]))
	ui.dashboard.tests.pressed.emit()
	check(commands.back() == ["testNode", {"subscriptionId":state.desiredSubscriptionId, "nodeId":state.desiredNodeId}], "Dashboard tested more than the selected identity")
	var job := {"pending":true, "nodeId":"node-a", "subscriptionId":state.desiredSubscriptionId, "results":[{"nodeId":"node-a", "status":"testing"}]}
	state.nodeTests = job
	ui.render(state)
	check(ui.dashboard.tests.text == "取消测速" and ui.dashboard.node_latency.text == "测速中", "Current probe feedback missing")
	check(ui.dashboard.connection.status.text == "未连接" and not ui.dashboard.connection.power.disabled, "Probe made connection busy")
	ui.dashboard.tests.pressed.emit()
	check(commands.back()[0] == "cancelNodeTest", "Single probe cancellation changed global scope")
	state.desiredNodeId = "node-b"
	ui.render(state)
	check(ui.dashboard.tests.text == "当前测速" and ui.dashboard.node_latency.text == "", "Old target's pending state leaked to newly selected node")
	ui.dashboard.tests.pressed.emit()
	check(commands.back()[0] == "testNode" and commands.back()[1].nodeId == "node-b", "New target did not start its own test")
	state.nodeTests = {"pending":true, "nodeId":"", "subscriptionId":"", "results":[]}
	ui.render(state)
	ui.dashboard.tests.pressed.emit()
	check(commands.back()[0] == "testNode", "Dashboard cancelled global scan instead of requesting selected target")
	state.desiredNodeId = ""
	ui.render(state)
	check(ui.dashboard.tests.disabled, "No selection still permits current-node testing")
	ui.change_section("subscriptions")
	ui.subscriptions.command.connect(func(method, params): commands.append([method, params]))
	await _settle()
	var card: PanelContainer = ui.subscriptions.list.get_child(0)
	check(ui.subscriptions.list.columns == 1, "Single subscription did not fill a row")
	check(ui.subscriptions.find_children("*", "Button", true, false).size() == 1, "Subscription page has actions beyond the shop entrance")
	check(not ui.subscriptions.find_children("*", "Label", true, false).any(func(label): return label.text.contains("本机当前选择")), "Subscription page still exposes local choice")
	ui.subscriptions.shop.pressed.emit()
	check(commands.back() == ["shop", {}], "Shop did not use owned native command")
	ui.action_error = ""
	state.subscriptions[0].usedBytes += 1000000000
	state.desiredSubscriptionId = "another"
	state.selectionPending = true
	ui.render(state)
	check(ui.subscriptions.list.get_child(0) == card and card.used.text == "已用 13.50 GB", "Metering/selection rebuilt cards or failed to refresh")
	for i in range(1, 3):
		state.subscriptions.append(state.subscriptions[0].merged({"id":"fixture-" + str(i), "templateName":"畅享流量包" if i == 1 else "专属带宽套餐", "quotaBytes":null if i == 1 else 500000000000, "status":"active" if i == 1 else "expired"}, true))
	for width in [320, 400, 600, 740, 850, 1040, 1440]:
		root.size = Vector2i(width, 820)
		state.platform = "android" if width < 740 else "windows"
		ui.render(state)
		await _settle()
		check(ui.scroll.get_h_scroll_bar().max_value <= ui.scroll.size.x + 2, "Subscription horizontal overflow at " + str(width))
		for child in ui.subscriptions.list.get_children():
			check(child.size.x >= 230, "Card columns too narrow at " + str(width))
			for label in child.find_children("*", "Label", true, false):
				check(label.get_global_rect().end.x <= child.get_global_rect().end.x + 1, "Text outside card at " + str(width))
		if width == 400: check(ui.subscriptions.list.columns == 1, "Phone must use one column")
		if width == 1040: check(ui.subscriptions.list.columns == 2, "Desktop should fit two columns")
		if width == 1440: check(ui.subscriptions.list.columns == 3, "Wide desktop should fit three columns")
		if width in [400, 1040, 1440]: await _capture("subscriptions-" + str(width))
	root.size = Vector2i(1040, 820)
	state.subscriptions = [state.subscriptions[0]]
	ui.render(state)
	await _capture("subscriptions-single")
	state.subscriptions = []
	ui.render(state)
	check(ui.subscriptions.empty.visible and ui.subscriptions.shop.visible, "Empty page lost its shop entrance")
	await _capture("subscriptions-empty")
	check(ui.bridge == null, "Offline test started a native runtime")
	print("PASS subscription and scoped probe regression" if failures.is_empty() else "FAIL subscription and scoped probe regression: %d" % failures.size())
	quit(0 if failures.is_empty() else 1)

func _settle() -> void:
	for _frame in range(12): await process_frame

func _capture(name: String) -> void:
	await _settle()
	if DisplayServer.get_name() == "headless": return
	RenderingServer.force_draw()
	root.get_texture().get_image().save_png(ProjectSettings.globalize_path("res://../.diagnostics/" + name + ".png"))
