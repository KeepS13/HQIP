extends SceneTree
## Offline-only layout/interaction checks. No bridge, account, socket or VPN is
## created. Fake subscription names below are fixtures, never production data.
const Main = preload("res://ui/main.gd")
var output := ""

func _initialize() -> void:
	output = ProjectSettings.globalize_path("res://../.diagnostics/godot-ui")
	DirAccess.make_dir_recursive_absolute(output)
	call_deferred("_run")

func _run() -> void:
	var utc := Main.S.unix_timestamp("2026-11-23T17:52:20Z")
	assert(utc == Main.S.unix_timestamp("2026-11-24T01:52:20.124773524+08:00"), "Positive UTC offsets must not freeze duration or extend expiry")
	assert(utc == Main.S.unix_timestamp("2026-11-23T10:52:20-07:00"), "Negative UTC offsets must use the same instant")
	assert(Main.S.unix_timestamp("") == 0)
	root.size = Vector2i(1040, 820)
	var ui := Main.new()
	ui.preview = true
	root.add_child(ui)
	ui.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	await _settle()
	assert(ui.bridge == null, "Offline preview must not create runtime")
	var nil_state := {"loggedIn":true,"subscriptions":null,"nodeChoices":null,"subscription":null,"metrics":{"history":null},"nodeTests":{"results":null},"updates":null,"autoStart":null}
	ui.render(nil_state)
	ui.picker.open(ui.state)
	await _settle()
	assert(ui.dashboard.chart.points.is_empty() and ui.picker.grid.get_child_count() == 0)
	assert(nil_state.subscriptions == null, "Rendering must not mutate bridge data")
	ui.picker.hide()
	ui.render({})
	await _capture("windows-login")
	var subscription := {"id":"test-subscription-001", "templateName":"标准流量包", "status":"active", "expiresAt":"2030-12-31T00:00:00Z", "quotaBytes":100000000000, "usedBytes":12500000000, "reservedBytes":500000, "bandwidthMbps":200, "occupied":1, "maxConnections":3}
	var fixture := {"loggedIn":true,"deviceName":"离线预览设备","platform":"windows","version":"5.0.0-preview.1","serverUrl":"https://hqip.sharevps.online","runState":"connected","connectionIntent":true,"desiredSubscriptionId":subscription.id,"desiredNodeId":"node-a","mode":"rule","tunEnabled":false,"subscription":subscription,"subscriptions":[subscription],"nodeChoices":[{"nodeId":"node-a","subscriptionId":subscription.id,"name":"香港 · 01","ready":true},{"nodeId":"node-b","subscriptionId":subscription.id,"name":"日本 · 02","ready":true}],"nodeTests":{"pending":false,"results":[{"nodeId":"node-a","status":"success","delayMs":36},{"nodeId":"node-b","status":"success","delayMs":72}]},"metrics":{"available":true,"stale":false,"uploadRate":230000,"downloadRate":2700000,"uploadBytes":150000000,"downloadBytes":910000000,"applicationConnections":12,"memoryBytes":43000000,"delayMs":45,"history":[]},"updates":{"ready":false,"message":"当前已是此渠道的可用版本"},"autoStart":{"supported":true,"enabled":false}}
	for index in range(60): fixture.metrics.history.append({"uploadRate":200000 + 80000 * sin(index / 4.0),"downloadRate":2100000 + 800000 * sin(index / 5.0)})
	ui.render(fixture)
	fixture.version = "9.8.7-test"
	fixture.coreVersion = "1.2.3-test"
	fixture.actualNodeId = "node-a"
	fixture.actualSubscriptionId = subscription.id
	ui.render(fixture)
	assert(ui.sidebar.version.text == "HQIP  v9.8.7-test" and ui.sidebar.core.text == "sing-box  v1.2.3-test", "Versions must come from runtime")
	ui.render(fixture.merged({"syncError":"临时同步失败"}, true))
	assert(ui.notice.visible)
	ui.render(fixture)
	assert(not ui.notice.visible, "Recovered sync must clear obsolete error")
	ui.toggle_menu()
	assert(not ui.sidebar.visible and ui.scroll.offset_left == 0, "Desktop also supports the same menu toggle")
	ui.toggle_menu()
	await _capture("windows-dashboard")
	ui.picker.open(fixture)
	await _capture("windows-nodes")
	assert(ui.picker.panel.size.y <= 572 and ui.picker.panel.position.y >= 18, "Desktop modal must fit and remain centered")
	ui.picker.search.text = "日本"
	ui.picker.search.text_changed.emit("日本")
	assert(ui.picker.grid.get_child_count() == 1, "Node filtering must match the selected subscription")
	ui.render(fixture)
	assert(ui.picker.search.text == "日本", "Polling must preserve search")
	ui.picker.hide()
	ui.change_section("subscriptions")
	await _capture("windows-subscriptions")
	ui.change_section("settings")
	await _capture("windows-settings")
	root.size = Vector2i(400, 820)
	fixture.platform = "android"
	ui.change_section("network")
	ui.render(fixture)
	await _capture("android-dashboard")
	assert(ui.narrow and is_equal_approx(ui.size.x, 400), "Physical window resizing must reflow the layout without changing its design canvas")
	assert(not ui.sidebar.visible and ui.menu_button.visible, "Phone starts with the shared drawer closed")
	assert(ui.dashboard.tun.visible and ui.dashboard.tun.disabled, "Android explains its mandatory TUN")
	ui.toggle_menu()
	await _capture("android-menu")
	assert(ui.sidebar.visible and ui.shade.visible and ui.drawer_open, "Hamburger opens the left drawer")
	assert(ui.sidebar.position.x == 0 and ui.sidebar.size.x < root.size.x)
	ui.sidebar.buttons[1].pressed.emit()
	assert(ui.section == "subscriptions" and not ui.drawer_open, "Navigation chooses page and dismisses drawer")
	ui.change_section("network")
	ui.toggle_menu()
	var escape := InputEventKey.new()
	escape.keycode = KEY_ESCAPE
	escape.pressed = true
	ui._unhandled_key_input(escape)
	assert(not ui.drawer_open, "Escape closes drawer")
	ui.toggle_menu()
	var click := InputEventMouseButton.new()
	click.pressed = true
	ui.shade.gui_input.emit(click)
	assert(not ui.drawer_open, "Scrim closes drawer")
	ui.picker.search.clear()
	ui.picker.open(ui.state)
	await _capture("android-nodes")
	assert(ui.picker.panel.position.y >= 18 and ui.picker.panel.size.x <= 372, "Phone modal must fit viewport")
	ui.picker.hide()
	for width in [320, 360, 600, 740, 1040]:
		root.size = Vector2i(width, 820)
		await _settle()
		assert(ui.narrow == (width < 740), "Window size must drive the drawer breakpoint")
		assert(ui.scroll.get_h_scroll_bar().max_value <= ui.scroll.size.x + 2, "Page must not overflow horizontally at " + str(width))
		ui.toggle_menu()
		await _settle()
		assert(ui.sidebar.position.x == 0, "All sizes keep the same left navigation")
		ui.toggle_menu()
	# A 150% display keeps the same logical layout instead of shrinking controls.
	root.content_scale_factor = 1.5
	root.size = Vector2i(600, 1230)
	await _settle()
	assert(ui.narrow and is_equal_approx(ui.size.x, 400), "DPI scaling must preserve responsive logical width")
	root.content_scale_factor = 1.0
	root.size = Vector2i(400, 820)
	ui.render({"platform":"android"})
	assert(ui.sidebar.version.text == "HQIP  —", "Missing version must not show a fabricated release")
	# Hidden pages intentionally skip polling renders. Exercise these metric
	# states on the visible dashboard, then restore the logged-out capture.
	ui.render({"platform":"android", "loggedIn":true})
	assert(ui.dashboard.values[3].text == "—", "Missing samples are unknown, not zero traffic")
	ui.render({"platform":"android", "loggedIn":true, "metrics":{"available":false,"uploadBytes":0,"downloadBytes":0,"updatedAt":null}})
	assert(ui.dashboard.values[3].text == "—", "Initial zero counters are not a measured sample")
	ui.render({"platform":"android", "loggedIn":true, "metrics":{"available":false,"uploadBytes":123,"downloadBytes":0,"updatedAt":"2026-11-23T00:00:00Z"}})
	assert(ui.dashboard.values[3].text == "123 B", "Preserve prior confirmed samples while unavailable")
	ui.render({"platform":"android"})
	await _capture("android-login")
	print("Offline UI checks passed; no native runtime was started.")
	quit()

func _settle() -> void:
	for _frame in range(6): await process_frame

func _capture(name: String) -> void:
	await _settle()
	if DisplayServer.get_name() == "headless": return
	# Low-CPU mode skips unchanged frames; force one only for the offline capture.
	RenderingServer.force_draw()
	var picture := root.get_texture().get_image()
	assert(picture.save_png(output.path_join(name + ".png")) == OK)
