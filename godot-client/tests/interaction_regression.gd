extends SceneTree
## Offline fixture: verify interaction geometry and independently owned states.
const Main = preload("res://ui/main.gd")
const Bridge = preload("res://runtime/bridge.gd")
var failures: Array[String] = []

class PipeFixture extends RefCounted:
	var sent: Array[Dictionary] = []
	func submit(raw: String) -> void: sent.append(JSON.parse_string(raw))
	func poll() -> String: return ""

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	root.size = Vector2i(1040, 820)
	var ui := Main.new()
	ui.preview = true
	root.add_child(ui)
	ui.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	var state: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	state.merge({"runState":"disconnected", "connectionIntent":false, "actualNodeId":"", "actualSubscriptionId":""}, true)
	ui.render(state)
	await process_frame
	for page in ["network", "subscriptions", "settings"]:
		ui.change_section(page)
		_check_surfaces(ui)
	ui.change_section("network")
	await _settle()
	var button: Button = ui.dashboard.connection.global_mode
	var height: float = ui.dashboard.connection.size.y
	var motion := InputEventMouseMotion.new()
	motion.position = button.get_global_rect().get_center()
	root.push_input(motion)
	await _settle()
	check(ui.dashboard.connection.size.y == height, "Actual hover resized connection card")
	button.grab_focus()
	var press := InputEventMouseButton.new()
	press.position = motion.position
	press.button_index = MOUSE_BUTTON_LEFT
	press.pressed = true
	root.push_input(press)
	await _settle()
	check(ui.dashboard.connection.size.y == height, "Actual press/focus resized connection card")
	motion.position = Vector2(1, 1)
	root.push_input(motion)
	press.position = motion.position
	press.pressed = false
	root.push_input(press)
	ui.picker.open(ui.state)
	_check_surfaces(ui)
	var first_node: Node = ui.picker.grid.get_child(0)
	first_node.grab_focus()
	state.subscriptions[0].usedBytes += 1024
	ui.render(state)
	check(first_node == ui.picker.grid.get_child(0) and first_node.has_focus(), "Metering refresh rebuilt/focused another node card")
	ui.picker.hide()
	ui.change_section("subscriptions")
	var first_plan: Node = ui.subscriptions.list.get_child(0)
	var plan_key: String = ui.subscriptions._key
	ui.change_section("network")
	state.subscriptions[0].usedBytes += 1024
	ui.render(state)
	check(ui.subscriptions._key == plan_key, "Hidden subscription page rendered during poll")
	ui.change_section("subscriptions")
	check(first_plan == ui.subscriptions.list.get_child(0), "Metering refresh destroyed the subscription row")
	ui.change_section("network")
	state.subscription = null
	state.desiredSubscriptionId = ""
	state.desiredNodeId = ""
	ui.render(state)
	check(ui.dashboard.plans.visible and ui.dashboard.plans.list.get_child_count() == state.subscriptions.size(), "Dashboard must display owned subscriptions without an authorized selection")
	check(not ui.dashboard.find_children("*", "Button", true, false).any(func(b): return b.text == "在本机使用此订阅"), "Subscription summary must not provide a second selection path")
	check(ui.dashboard.tests.disabled, "Current-node test requires a selected node")
	ui.scroll.scroll_vertical = 1200
	await _settle()
	if DisplayServer.get_name() != "headless":
		RenderingServer.force_draw()
		root.get_texture().get_image().save_png(ProjectSettings.globalize_path("res://../.diagnostics/ux-subscriptions.png"))
	ui.scroll.scroll_vertical = 0
	ui.pending["refresh"] = true
	ui.render(state)
	check(not ui.dashboard.node_button.disabled, "Account refresh must not lock the node picker")
	ui.pending["node"] = true
	ui.render(state.merged({"desiredNodeId":"node-a"}, true))
	check(ui.dashboard.connection.power.disabled and ui.dashboard.connection.status.text == "未连接", "Connect must wait for local save acknowledgement without presenting a connection transition")
	ui._reply("node", {"desiredSubscriptionId":"test-subscription-001", "desiredNodeId":"node-b", "subscription":state.subscriptions[0]}, "")
	check(ui.dashboard.node_name.text == "日本 · 02" or ui.dashboard.node_name.text.contains("日本"), "Native acknowledgement must immediately display the selected node")
	check(ui.dashboard.connection.status.text == "未连接" and not ui.pending.has("node"), "Choosing a node presented a connection transition")
	check(not ui.dashboard.connection.power.disabled, "Confirmed choice did not enable Connect")
	ui.render(state.merged({"loggedIn":false}, true))
	_check_surfaces(ui)
	_check_reply_order()
	print("PASS interaction regression" if failures.is_empty() else "FAIL interaction regression: %d" % failures.size())
	quit(0 if failures.is_empty() else 1)

func _check_surfaces(ui: Node) -> void:
	for button in ui.find_children("*", "Button", true, false):
		var normal: Vector2 = button.get_theme_stylebox("normal").get_minimum_size()
		for mode in ["hover", "pressed", "hover_pressed", "disabled", "focus"]:
			check(button.get_theme_stylebox(mode).get_minimum_size() == normal, "%s changes geometry in %s" % [button.get_path(), mode])

func _settle() -> void:
	for _frame in range(6): await process_frame

func _check_reply_order() -> void:
	var bridge := Bridge.new()
	var pipe := PipeFixture.new()
	bridge._plugin = pipe
	var received := []
	bridge.replied.connect(func(method, result, _error): received.append([method, result]))
	bridge.call_command("state")
	bridge.call_command("node", {"nodeId":"new"})
	bridge._receive(JSON.stringify({"id":2, "result":{"desiredNodeId":"new"}}))
	bridge._receive(JSON.stringify({"id":1, "result":{"desiredNodeId":"old"}}))
	check(received.size() == 1 and pipe.sent.size() == 2, "Stale poll overwrote an acknowledged choice or created a redundant query")
	bridge.call_command("state") # The normal timer owns the next periodic read.
	bridge._receive(JSON.stringify({"id":3, "result":{"desiredNodeId":"new"}}))
	check(received.size() == 2 and received[1][1].desiredNodeId == "new", "Fresh state was not delivered")
	bridge.free()
