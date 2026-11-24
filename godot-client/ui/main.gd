extends Control
## Presentation shell mirrors Wails navigation. Offline previews never construct
## the native bridge, so layout checks cannot log in or connect a VPN.
const S = preload("res://ui/style.gd")
const NativeBridge = preload("res://runtime/bridge.gd")
const LocalSelection = preload("res://runtime/local_selection.gd")
const Login = preload("res://ui/login.gd")
const Dashboard = preload("res://ui/dashboard.gd")
const Subscriptions = preload("res://ui/subscriptions.gd")
const Settings = preload("res://ui/settings.gd")
const NodePicker = preload("res://ui/node_picker.gd")
const Tray = preload("res://runtime/tray.gd")
const Navigation = preload("res://ui/navigation.gd")
const WindowChrome = preload("res://ui/window_chrome.gd")
var preview := false
var bridge: Node
var state: Dictionary = {}
var local_selection := LocalSelection.new()
var section := "network"
var sidebar: PanelContainer
var menu_button: Button
var scroll: ScrollContainer
var shade: ColorRect
var drawer_open := false
var desktop_sidebar := true
var narrow := false
var margin: MarginContainer
var eyebrow: Label
var heading: Label
var device: Label
var notice: Label
var login: VBoxContainer
var login_frame: PanelContainer
var dashboard: VBoxContainer
var subscriptions: VBoxContainer
var settings: VBoxContainer
var picker: Control
var help: VBoxContainer
var pending: Dictionary = {}
var action_error := ""
var runtime_error := ""
var _startup_marked := false
var tray: Node
var chrome: Control
var header: HBoxContainer
var device_chip: PanelContainer
var login_margin: MarginContainer

func _ready() -> void:
	theme = S.theme()
	RenderingServer.set_default_clear_color(S.BG)
	OS.low_processor_usage_mode = true
	Engine.max_fps = 30
	if OS.get_name() == "Android":
		get_tree().quit_on_go_back = false
	_build()
	_configure_scale()
	get_window().dpi_changed.connect(_configure_scale)
	resized.connect(_layout)
	_layout()
	if preview: return
	tray = Tray.new()
	add_child(tray)
	tray.command.connect(_command)
	bridge = NativeBridge.new()
	add_child(bridge)
	bridge.replied.connect(_reply)
	var error: String = bridge.start()
	if error != "": runtime_error = error; render(state)
	else: bridge.call_command("state")
	var timer := Timer.new()
	timer.wait_time = 1
	timer.timeout.connect(func(): bridge.call_command("state"))
	add_child(timer)
	timer.start()

func _configure_scale() -> void:
	# A fixed design canvas only shrinks when the native window is resized, so
	# responsive breakpoints never run. Use density-scaled logical pixels instead:
	# Windows uses 96 DPI, Android uses 160 DPI; previews keep deterministic pixels.
	var window := get_window()
	var scale_factor := 1.0
	if not preview:
		var density := DisplayServer.screen_get_dpi(window.current_screen)
		scale_factor = maxf(1.0, density / (160.0 if OS.get_name() == "Android" else 96.0))
	window.content_scale_size = Vector2i.ZERO
	window.content_scale_mode = Window.CONTENT_SCALE_MODE_DISABLED
	window.content_scale_factor = scale_factor
	if not preview and OS.get_name() == "Windows":
		window.min_size = Vector2i(Vector2(320, 480) * scale_factor)

func _build() -> void:
	scroll = ScrollContainer.new()
	scroll.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
	scroll.vertical_scroll_mode = ScrollContainer.SCROLL_MODE_SHOW_ALWAYS
	add_child(scroll)
	margin = MarginContainer.new()
	margin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	scroll.add_child(margin)
	var page := VBoxContainer.new()
	page.add_theme_constant_override("separation", 0)
	margin.add_child(page)
	header = HBoxContainer.new()
	header.custom_minimum_size.y = 63
	header.add_theme_constant_override("separation", 12)
	menu_button = S.button("", toggle_menu)
	menu_button.icon = preload("res://assets/menu.svg")
	menu_button.custom_minimum_size = Vector2(48, 48)
	menu_button.tooltip_text = "展开或收起左侧菜单"
	header.add_child(menu_button)
	var title := VBoxContainer.new()
	title.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	title.alignment = BoxContainer.ALIGNMENT_END
	title.add_theme_constant_override("separation", 9)
	eyebrow = S.text("HQIP / OVERVIEW", 9, Color("6d8098"))
	heading = S.text("网络仪表盘", 25)
	S.typography(heading, 600)
	S.typography(eyebrow, 600, 2)
	title.add_child(eyebrow)
	title.add_child(heading)
	header.add_child(title)
	device_chip = PanelContainer.new()
	device_chip.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	device_chip.add_theme_stylebox_override("panel", S.panel(Color("161f2b"), 20, 8, Color("263345")))
	var device_row := HBoxContainer.new()
	device_row.add_theme_constant_override("separation", 8)
	device_row.add_child(S.icon("Laptop", 16, Color("a7b7c9")))
	device = S.text("账户未登录", 10, Color("a7b7c9"))
	device.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	device.autowrap_mode = TextServer.AUTOWRAP_OFF
	device.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
	device_row.add_child(device)
	device_chip.add_child(device_row)
	header.add_child(device_chip)
	page.add_child(header)
	S.space(page, 28)
	notice = S.text("", 13, S.BAD)
	page.add_child(notice)
	login_margin = S.inset(page, 0, 44, 0, 0)
	login_frame = PanelContainer.new()
	login_frame.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
	var login_box := S.panel(Color("141e2b"), 16, 28, Color("2b3a49"))
	login_box.content_margin_top = 38
	login_box.content_margin_bottom = 30
	login_frame.add_theme_stylebox_override("panel", login_box)
	login_margin.add_child(login_frame)
	login = Login.new()
	login_frame.add_child(login)
	S.glow(login_frame, Color("141e2b"), Color("233c3e70"), Vector2(0.5, 0))
	dashboard = Dashboard.new()
	page.add_child(dashboard)
	subscriptions = Subscriptions.new()
	page.add_child(subscriptions)
	settings = Settings.new()
	page.add_child(settings)
	for view in [login, dashboard, subscriptions, settings]: view.command.connect(_command)
	dashboard.choose_node.connect(func(): picker.open(local_selection.present(state)))
	help = S.card(page, "使用提示")
	help.add_child(S.text("选择节点及对应订阅后，点击“连接”开启代理。未连接时只保存本机选择；连接中切换节点会先关闭旧连接，再连接新目标。", 13, S.MUTED))
	help.add_child(S.text("订阅到期、流量耗尽或被移除时，VPN 会关闭，不自动使用另一份订阅。连接失败时可手动重连。", 13, S.MUTED))
	help.add_child(S.text("每次连接成功后自动检测当前连接的延迟。节点列表及其他测速需手动触发，测试失败不会断开连接。", 13, S.MUTED))
	help.get_parent().hide()
	shade = ColorRect.new()
	shade.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	shade.color = Color(0.02, 0.03, 0.05, 0.72)
	shade.gui_input.connect(func(event):
		if event is InputEventMouseButton and event.pressed: close_menu())
	add_child(shade)
	sidebar = Navigation.new()
	add_child(sidebar)
	sidebar.selected.connect(change_section)
	sidebar.close_requested.connect(toggle_menu)
	sidebar.help_requested.connect(func():
		help.get_parent().visible = not help.get_parent().visible
		close_menu())
	chrome = WindowChrome.new()
	chrome.native_enabled = not preview
	add_child(chrome)
	picker = NodePicker.new()
	add_child(picker)
	move_child(chrome, get_child_count() - 1)
	picker.command.connect(_command)
	render({})

func _layout() -> void:
	var next_narrow := size.x < 740
	if next_narrow != narrow:
		drawer_open = false
		sidebar.release_focus_loop()
	narrow = next_narrow
	var side_width := minf(280, size.x - 48) if narrow else 175.0 if size.x <= 850 else 206.0
	var top := 34.0 if chrome != null and chrome.visible else 0.0
	sidebar.position = Vector2.ZERO
	sidebar.size = Vector2(side_width, size.y)
	sidebar.visible = drawer_open if narrow else desktop_sidebar
	sidebar.close_button.visible = narrow
	shade.visible = narrow and drawer_open
	scroll.offset_left = 0 if narrow or not desktop_sidebar else side_width
	scroll.offset_top = top if narrow else 0
	var gutter := 18 if narrow else 24 if size.x <= 850 else 35
	for edge in ["left", "right"]: margin.add_theme_constant_override("margin_" + edge, gutter)
	margin.add_theme_constant_override("margin_top", 18 if narrow else 40 if size.x <= 850 else 43)
	margin.add_theme_constant_override("margin_bottom", 22)
	login_frame.custom_minimum_size.x = maxf(0, minf(580, size.x - scroll.offset_left - 2 * gutter - 8))
	login_margin.add_theme_constant_override("margin_top", 0 if size.x <= 850 else 16)
	device_chip.visible = not narrow and size.x - scroll.offset_left >= 560
	menu_button.visible = narrow or not desktop_sidebar
	heading.add_theme_font_size_override("font_size", 21 if narrow else 25)

func toggle_menu() -> void:
	if narrow:
		drawer_open = not drawer_open
	else:
		desktop_sidebar = not desktop_sidebar
	_layout()
	if drawer_open: sidebar.focus_drawer()
	else: sidebar.release_focus_loop(); menu_button.grab_focus()

func close_menu() -> void:
	if not drawer_open: return
	drawer_open = false
	sidebar.release_focus_loop()
	_layout()
	menu_button.grab_focus()

func _unhandled_key_input(event: InputEvent) -> void:
	if drawer_open and event.is_action_pressed("ui_cancel"):
		close_menu()
		get_viewport().set_input_as_handled()

func _notification(what: int) -> void:
	if what != NOTIFICATION_WM_GO_BACK_REQUEST: return
	if picker != null and picker.visible: picker.hide()
	elif drawer_open: close_menu()
	elif Engine.has_singleton("HQIPRuntime"): Engine.get_singleton("HQIPRuntime").background()

func change_section(value: String) -> void:
	section = value
	close_menu()
	scroll.scroll_vertical = 0
	render(state)

func _command(method: String, params: Dictionary = {}) -> void:
	if preview:
		action_error = "离线界面预览：未执行网络或账户操作"
		render(state)
		return
	if method in ["connect", "testNode"] and local_selection.busy(): return
	if method == "logout": local_selection.clear()
	if method == "node" and not pending.has("logout") and local_selection.stage(state, params):
		action_error = ""
		_flush_local_selection()
		render(state)
		return
	if pending.has(method): return
	action_error = ""
	if bridge.call_command(method, params): pending[method] = true
	else: runtime_error = "客户端运行时未连接"
	render(state)

func _flush_local_selection() -> void:
	var params: Dictionary = local_selection.take_next(state)
	if params.is_empty(): return
	if bridge.call_command("node", params): pending["node"] = true
	else:
		local_selection.clear()
		runtime_error = "客户端运行时未连接"

func _reply(method: String, result: Variant, error: String) -> void:
	pending.erase(method)
	if method == "node" and local_selection.busy():
		# Keep rendering the last click while an earlier save is acknowledged.
		# Failure rolls back to the last native snapshot only if still current.
		var superseded := local_selection.finish()
		if error == "" and result is Dictionary: state.merge(result, true)
		elif not superseded: action_error = error
		_flush_local_selection()
		render(state)
		return
	if method == "node" and (pending.has("logout") or not state.get("loggedIn", false)): return
	if error != "":
		if method in ["state", "runtime"]: runtime_error = error
		else: action_error = error
		render(state)
		return
	if method == "state" and result is Dictionary:
		runtime_error = ""
		render(result)
		_mark_started()
	elif method in ["node", "subscription"] and result is Dictionary:
		# Native acknowledgement includes the persisted choice. Display it now;
		# do not wait for the next one-second poll or mark connection as pending.
		state.merge(result, true)
		render(state)
	elif method == "installUpdate" and state.get("platform", "") == "windows" and result is Dictionary and result.get("ready", false):
		get_tree().quit()
	elif method in ["shop", "account"]:
		pass # Browser navigation changes no client state and needs no extra poll.
	else: bridge.call_command("state")

func render(value: Dictionary) -> void:
	# Go nil collections encode as JSON null. Normalize only container fields;
	# null quota/expiry/latency values retain their business meaning. Views own
	# this copy and cannot mutate the bridge response or an offline fixture.
	state = value.duplicate(true)
	chrome.visible = state.get("platform", OS.get_name().to_lower()) == "windows"
	_layout()
	state["commandPending"] = pending.duplicate()
	for key in ["nodeChoices", "subscriptions"]:
		if not state.get(key) is Array: state[key] = []
	for key in ["metrics", "nodeTests", "updates", "autoStart"]:
		if not state.get(key) is Dictionary: state[key] = {}
	if not state.metrics.get("history") is Array: state.metrics.history = []
	if not state.nodeTests.get("results") is Array: state.nodeTests.results = []
	var logged: bool = state.get("loggedIn", false)
	if not logged: local_selection.clear()
	var shown: Dictionary = local_selection.present(state)
	login_frame.visible = not logged and section != "settings"
	login_margin.visible = login_frame.visible
	dashboard.visible = logged and section == "network"
	subscriptions.visible = logged and section == "subscriptions"
	settings.visible = section == "settings"
	heading.text = {"network":"网络仪表盘", "subscriptions":"我的订阅", "settings":"连接设置"}.get(section, "网络仪表盘")
	eyebrow.text = "HQIP / " + {"network":"OVERVIEW", "subscriptions":"SUBSCRIPTIONS", "settings":"PREFERENCES"}.get(section, "OVERVIEW")
	device.text = str(state.get("deviceName", "本机")) if logged else "账户未登录"
	device_chip.custom_minimum_size.x = clampf(device.get_theme_font("font").get_string_size(device.text, HORIZONTAL_ALIGNMENT_LEFT, -1, 10).x + 44, 100, 180)
	sidebar.render(shown, section)
	# Hidden pages retain their local input/expansion state. Render their latest
	# snapshot when opened, avoiding chart/layout/list work on every idle poll.
	if login_margin.visible: login.render(state)
	for view in [dashboard, subscriptions, settings]:
		if view.visible: view.render(shown)
	if tray != null and tray.indicator >= 0:
		tray.render(shown)
		settings.close_note.text = "关闭窗口后继续在托盘运行；托盘中“退出并恢复网络”会完全退出。"
	if picker.visible: picker.render(shown)
	var error: String = action_error if not action_error.is_empty() else runtime_error
	if error == "": error = state.get("lastError", "")
	if error == "": error = state.get("syncError", "")
	notice.text = error
	notice.visible = not notice.text.is_empty()

func _mark_started() -> void:
	if _startup_marked: return
	_startup_marked = true
	for argument in OS.get_cmdline_user_args():
		if argument.begins_with("--startup-marker="):
			var marker := FileAccess.open(argument.trim_prefix("--startup-marker="), FileAccess.WRITE)
			if marker != null: marker.store_string("ready"); marker.close()
