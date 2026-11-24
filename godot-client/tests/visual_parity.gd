extends SceneTree
## Same data as capture_client_reference.cjs. These screenshots are isolated
## presentation evidence: never instantiate the bridge or claim VPN acceptance.
const Main = preload("res://ui/main.gd")
var ui: Control
var fixture: Dictionary
var output: String
var bounds: Dictionary = {}
var commands: Array = []

func _initialize() -> void:
	call_deferred("_run")

func _run() -> void:
	output = ProjectSettings.globalize_path("res://../.diagnostics/style-replica/new")
	DirAccess.make_dir_recursive_absolute(output)
	fixture = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	root.size = Vector2i(1040, 820)
	ui = Main.new()
	ui.preview = true
	root.add_child(ui)
	ui.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	ui.render(fixture)
	await _capture("dashboard")
	assert(ui.bridge == null)
	# Original reference: node x=241/y=134/w=756; connection y=329;
	# traffic y=523.59. Tolerances allow fractional CSS-to-Control rounding.
	assert(absf(ui.dashboard.node_panel.global_position.x - 241) <= 1)
	assert(absf(ui.dashboard.node_panel.global_position.y - 134) <= 2)
	assert(absf(ui.dashboard.connection.global_position.y - 329) <= 2)
	assert(absf(ui.dashboard.traffic.global_position.y - 523.59) <= 2)
	ui.scroll.scroll_vertical = 1000
	await _capture("dashboard-bottom")
	ui.scroll.scroll_vertical = 0
	ui.picker.open(ui.state)
	await _capture("nodes")
	assert(ui.picker.panel.size.x == 820)
	assert(ui.picker.panel.size.y <= 334 and absf(ui.picker.panel.position.y - 252.2) <= 2, "First modal open must fit its contents")
	ui.picker.command.connect(func(method, params): commands.append([method, params]))
	ui.picker.search.text = "日本"
	ui.picker.search.text_changed.emit("日本")
	assert(ui.picker.grid.get_child_count() == 1)
	ui.render(fixture)
	assert(ui.picker.search.text == "日本")
	ui.picker.grid.get_child(0).pressed.emit()
	assert(commands.back() == ["node", {"nodeId":"node-b", "subscriptionId":"test-subscription-001"}])
	assert(not ui.picker.visible)
	ui.action_error = ""
	ui.change_section("subscriptions")
	await _capture("subscriptions")
	var card: PanelContainer = ui.subscriptions.list.get_child(0)
	ui.render(fixture)
	assert(ui.subscriptions.list.get_child(0) == card and card.remaining.visible)
	assert(ui.subscriptions.shop.visible and ui.subscriptions.list.columns == 1)
	ui.change_section("settings")
	await _capture("settings")
	ui.change_section("network")
	var logged_out := fixture.merged({"loggedIn":false, "runState":"disconnected"}, true)
	ui.render(logged_out)
	await _capture("login")
	assert(absf(ui.login_frame.global_position.y - 150) <= 2, "Login top: " + str(ui.login_frame.global_position.y))
	ui.login.service.show()
	await _capture("login-service")
	ui.login.address.text = "https://example.invalid"
	ui.login.address.text_changed.emit(ui.login.address.text)
	ui.render(logged_out)
	assert(ui.login.address.text == "https://example.invalid")
	ui.login.service.hide()
	for run in ["disconnected", "connecting", "recovering", "failed"]:
		ui.render(fixture.merged({"runState":run, "connectionIntent":run in ["connecting", "recovering"]}, true))
		await _capture(run)
		if run in ["connecting", "recovering"]: assert(not ui.dashboard.connection.power.disabled)
	for width in [320, 400, 600, 740, 850, 1040, 1440]:
		root.size = Vector2i(width, 820)
		for section in ["network", "subscriptions", "settings"]:
			ui.render(fixture.merged({"platform":"android" if width < 740 else "windows"}, true))
			ui.change_section(section)
			await _settle()
			assert(ui.scroll.get_h_scroll_bar().max_value <= ui.scroll.size.x + 2, "Overflow: %s at %d" % [section, width])
			if width == 400: await _capture("phone-" + section)
		ui.picker.search.clear()
		ui.picker.open(ui.state)
		await _settle()
		assert(ui.picker.panel.position.x >= 0 and ui.picker.panel.get_rect().end.x <= width, "Modal bounds at " + str(width))
		ui.picker.hide()
	FileAccess.open(output.path_join("bounds.json"), FileAccess.WRITE).store_string(JSON.stringify(bounds, "\t"))
	print("PASS visual reference geometry, node selection, state persistence, four connection states and seven viewport widths; native bridge absent.")
	quit()

func _settle() -> void:
	for _frame in range(8): await process_frame

func _capture(name: String) -> void:
	await _settle()
	var targets := {"header":ui.header, "node":ui.dashboard.node_panel, "connection":ui.dashboard.connection, "traffic":ui.dashboard.traffic, "summary":ui.dashboard.summary, "login":ui.login_frame, "modal":ui.picker.panel}
	bounds[name] = {}
	for key in targets:
		var rect: Rect2 = targets[key].get_global_rect()
		bounds[name][key] = [rect.position.x, rect.position.y, rect.size.x, rect.size.y]
	if DisplayServer.get_name() == "headless": return
	RenderingServer.force_draw()
	assert(root.get_texture().get_image().save_png(output.path_join(name + ".png")) == OK)
