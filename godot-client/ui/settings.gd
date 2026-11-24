extends VBoxContainer
## Original two-column settings rows. Native state owns preferences and updater;
## UI-specific layout never changes platform VPN/autostart behavior.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
const Toggle = preload("res://ui/toggle.gd")
var state: Dictionary = {}
var account: Label
var account_rows: VBoxContainer
var version: Label
var update_status: Label
var install: Button
var check: Button
var logout: Button
var auto_start: Button
var startup_note: Label
var close_note: Label
var tun: Button
var modes: Array[Button] = []
var title: Label
var account_values: Array[Label] = []
var startup_row: HBoxContainer
var tun_note: Label

func _ready() -> void:
	var card := S.card(self)
	S.space(card, 15)
	title = S.text("账户与连接设置", 18)
	card.add_child(title)
	S.space(card, 15)
	S.line(card, Color("24303d"))
	startup_row = _mode_row(card, "开机自启", "登录 Windows 后自动打开 HQIP，VPN 连接仍需手动开启。")
	startup_note = startup_row.get_child(0).get_child(1)
	auto_start = Toggle.new()
	auto_start.tooltip_text = "开机自启"
	auto_start.toggled.connect(func(enabled): command.emit("autoStart", {"enabled":enabled}))
	startup_row.add_child(auto_start)
	account_rows = VBoxContainer.new()
	account_rows.add_theme_constant_override("separation", 0)
	card.add_child(account_rows)
	for name in ["用户账户", "控制服务", "配置 / 规则版本"]:
		var margin := S.inset(account_rows, 0, 21, 0, 21)
		var row := HBoxContainer.new()
		row.add_theme_constant_override("separation", 10)
		margin.add_child(row)
		var label := S.text(name, 12, Color("8095ac"))
		label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
		label.autowrap_mode = TextServer.AUTOWRAP_OFF
		row.add_child(label)
		var value := S.text("", 12)
		value.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
		row.add_child(value)
		account_values.append(value)
		S.line(account_rows, Color("283442"))
	account = account_values[0]
	var mode_row := _mode_row(account_rows, "路由模式", "规则模式保留大陆与指定服务直连。")
	var frame := PanelContainer.new()
	frame.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	frame.add_theme_stylebox_override("panel", S.panel(Color("0d1520"), 8, 4, Color("293646")))
	mode_row.add_child(frame)
	var group := HBoxContainer.new()
	group.add_theme_constant_override("separation", 0)
	frame.add_child(group)
	for entry in [["rule", "规则"], ["global", "全局"]]:
		var key: String = entry[0]
		var button := S.button(entry[1], func(): command.emit("mode", {"mode":key, "tun":state.get("tunEnabled", false)}))
		button.set_meta("mode", key)
		button.custom_minimum_size = Vector2(40, 29)
		button.add_theme_font_size_override("font_size", 10)
		modes.append(button)
		group.add_child(button)
	S.line(account_rows, Color("24303d"))
	var tun_row := _mode_row(account_rows, "TUN 虚拟网卡", "关闭时使用系统代理。首次启用需要管理员授权。")
	tun_note = tun_row.get_child(0).get_child(1)
	tun = Toggle.new()
	tun.tooltip_text = "TUN 虚拟网卡"
	tun.toggled.connect(func(enabled): command.emit("mode", {"mode":state.get("mode", "rule"), "tun":enabled}))
	tun_row.add_child(tun)
	S.space(card, 11)
	close_note = S.text("", 11, Color("91a3b9"))
	close_note.custom_minimum_size.y = 21
	close_note.add_theme_constant_override("line_spacing", 5)
	card.add_child(close_note)
	S.space(card, 20)
	S.line(card, Color("293447"))
	var update_margin := S.inset(card, 0, 20, 0, 20)
	var updates := HBoxContainer.new()
	updates.add_theme_constant_override("separation", 18)
	update_margin.add_child(updates)
	var copy := VBoxContainer.new()
	copy.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	copy.add_theme_constant_override("separation", 7)
	updates.add_child(copy)
	version = S.text("HQIP 客户端", 13)
	copy.add_child(version)
	update_status = S.text("", 12, Color("91a3b9"))
	copy.add_child(update_status)
	check = S.button("检查更新", func(): command.emit("updates", {}))
	S.flat_button(check)
	check.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	updates.add_child(check)
	install = S.button("退出，下次启动更新", func(): command.emit("installUpdate", {}), true)
	card.add_child(install)
	S.space(card, 25)
	logout = S.button("退出用户账户", func(): command.emit("logout", {}))
	logout.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	logout.add_theme_font_size_override("font_size", 11)
	S.button_surface(logout, S.panel(Color.TRANSPARENT, 8, 11, Color("6d4948")))
	logout.add_theme_color_override("font_color", Color("d9a59b"))
	card.add_child(logout)
	S.space(card, 5)

func _mode_row(parent: Node, name: String, description: String) -> HBoxContainer:
	var margin := S.inset(parent, 0, 15, 0, 17)
	var row := HBoxContainer.new()
	row.add_theme_constant_override("separation", 10)
	margin.add_child(row)
	var copy := VBoxContainer.new()
	copy.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	copy.add_theme_constant_override("separation", 6)
	row.add_child(copy)
	copy.add_child(S.text(name, 12))
	var note := S.text(description, 11, Color("7f90a5"))
	note.custom_minimum_size.y = 21
	note.add_theme_constant_override("line_spacing", 4)
	copy.add_child(note)
	return row

func render(value: Dictionary) -> void:
	state = value
	var logged: bool = state.get("loggedIn", false)
	var mobile: bool = state.get("platform", "") == "android"
	var pending: Dictionary = state.get("commandPending", {})
	var changing: bool = state.get("runState", "") == "connecting" or (state.get("selectionPending", false) and state.get("connectionIntent", false)) or pending.has("mode")
	title.text = "账户与连接设置" if logged else "客户端设置"
	account_rows.visible = logged
	logout.visible = logged
	startup_row.get_parent().visible = not mobile
	var startup: Dictionary = state.get("autoStart", {})
	auto_start.disabled = not startup.get("supported", false) or pending.has("autoStart")
	auto_start.set_pressed_no_signal(startup.get("enabled", false))
	auto_start.queue_redraw()
	startup_note.text = startup.get("error", "") if startup.get("error", "") != "" else "登录 Windows 后自动打开 HQIP，VPN 连接仍需手动开启。"
	account_values[0].text = str(state.get("deviceName", "—"))
	account_values[1].text = str(state.get("serverUrl", "—"))
	account_values[2].text = "v%d / v%d" % [int(state.get("appliedConfigRevision", 0)), int(state.get("rulesVersion", 0))]
	tun_note.text = "Android 使用系统 VPN 接管连接。" if mobile else "关闭时使用系统代理。首次启用需要管理员授权。"
	tun.disabled = mobile or changing
	tun.set_pressed_no_signal(state.get("tunEnabled", false))
	tun.queue_redraw()
	for button in modes:
		var selected: bool = button.get_meta("mode") == state.get("mode", "rule")
		button.disabled = changing or selected
		S.button_surface(button, S.panel(Color("2c413f") if selected else Color.TRANSPARENT, 5, 5, Color.TRANSPARENT))
		button.add_theme_color_override("font_disabled_color", Color("b3d7c8") if selected else S.MUTED)
	var updates: Dictionary = state.get("updates", {})
	version.text = "HQIP 客户端  " + S.version(state.get("version", ""))
	update_status.text = "正在检查并准备更新…" if updates.get("checking", false) else str(updates.get("message", "登录后自动检查，每 6 小时检查一次"))
	check.disabled = not logged or updates.get("checking", false) or pending.has("updates")
	logout.disabled = pending.has("logout")
	install.disabled = pending.has("installUpdate")
	install.visible = updates.get("ready", false)
	install.text = "安装已下载的更新" if mobile else "退出，下次启动更新"
	close_note.text = "返回桌面后 VPN 继续运行；点击“断开连接”停止。" if mobile else "关闭窗口后继续在托盘运行；托盘中“退出并恢复网络”会完全退出。"
