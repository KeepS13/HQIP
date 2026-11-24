extends PanelContainer
## Connection status and compact routing controls. Native state owns connection
## intent; this component only emits commands and never optimistically connects.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
const Toggle = preload("res://ui/toggle.gd")
var state: Dictionary = {}
var status: Label
var dot: Label
var caption: Label
var detail: Label
var power: Button
var rule: Button
var global_mode: Button
var tun: Button
var tun_label: Label
var controls: HFlowContainer
var mode_label: Label
var glow: ColorRect

func _ready() -> void:
	var box := S.panel(Color("151f2b"), 12, 20, Color("2d3c4c"))
	box.content_margin_top = 18
	box.content_margin_bottom = 18
	add_theme_stylebox_override("panel", box)
	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 0)
	add_child(column)
	var row := HBoxContainer.new()
	row.add_theme_constant_override("separation", 20)
	column.add_child(row)
	var info := VBoxContainer.new()
	info.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	info.add_theme_constant_override("separation", 6)
	row.add_child(info)
	var labels := HBoxContainer.new()
	labels.add_theme_constant_override("separation", 14)
	var label := S.text("本机连接", 10, Color("92a2b7"))
	label.autowrap_mode = TextServer.AUTOWRAP_OFF
	label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	labels.add_child(label)
	caption = S.text("", 10, Color("9bb6b4"))
	labels.add_child(caption)
	info.add_child(labels)
	var status_row := HBoxContainer.new()
	status_row.add_theme_constant_override("separation", 10)
	dot = S.text("●", 13, S.GOOD)
	dot.autowrap_mode = TextServer.AUTOWRAP_OFF
	dot.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	status_row.add_child(dot)
	status = S.text("未连接", 23)
	status_row.add_child(status)
	info.add_child(status_row)
	detail = S.text("", 11, Color("8b9db1"))
	detail.autowrap_mode = TextServer.AUTOWRAP_OFF
	detail.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
	detail.custom_minimum_size.y = 18
	info.add_child(detail)
	power = S.button("连接", func(): command.emit("disconnect" if _can_stop() else "connect", {}))
	power.custom_minimum_size = Vector2(106, 44)
	power.add_theme_font_size_override("font_size", 11)
	power.icon = load("res://assets/icons/Power.svg")
	power.add_theme_constant_override("icon_max_width", 20)
	power.add_theme_constant_override("h_separation", 8)
	power.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	row.add_child(power)
	S.space(column, 13)
	S.line(column, Color("42645860"))
	S.space(column, 11)
	controls = HFlowContainer.new()
	controls.add_theme_constant_override("h_separation", 10)
	column.add_child(controls)
	mode_label = S.text("路由模式", 10, Color("91a6ad"))
	mode_label.autowrap_mode = TextServer.AUTOWRAP_OFF
	mode_label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	mode_label.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	controls.add_child(mode_label)
	var frame := PanelContainer.new()
	frame.add_theme_stylebox_override("panel", S.panel(Color("0e1b2170"), 8, 3, Color("40595d")))
	controls.add_child(frame)
	var modes := HBoxContainer.new()
	modes.add_theme_constant_override("separation", 3)
	frame.add_child(modes)
	rule = S.button("规则", func(): _mode("rule", state.get("tunEnabled", false)))
	global_mode = S.button("全局", func(): _mode("global", state.get("tunEnabled", false)))
	for button in [rule, global_mode]:
		button.custom_minimum_size = Vector2(64, 29)
		button.add_theme_font_size_override("font_size", 11)
		button.icon = load("res://assets/icons/" + ("Network" if button == rule else "Globe2") + ".svg")
		button.add_theme_constant_override("icon_max_width", 13)
		modes.add_child(button)
	var tun_frame := PanelContainer.new()
	tun_frame.add_theme_stylebox_override("panel", S.panel(Color("0e1b2170"), 8, 5, Color("40595d")))
	controls.add_child(tun_frame)
	var tun_row := HBoxContainer.new()
	tun_row.add_theme_constant_override("separation", 9)
	tun_frame.add_child(tun_row)
	tun_label = S.text("TUN", 11, Color("a9bdc3"))
	tun_label.autowrap_mode = TextServer.AUTOWRAP_OFF
	tun_row.add_child(tun_label)
	tun = Toggle.new()
	tun.compact = true
	tun.tooltip_text = "TUN 虚拟网卡"
	tun.toggled.connect(func(on): _mode(state.get("mode", "rule"), on))
	tun_row.add_child(tun)
	glow = S.glow(self, Color("151f2b"), Color("1e3d3c80"))
	S.typography(status, 500)
	resized.connect(func(): mode_label.visible = get_viewport_rect().size.x > 850)

func _can_stop() -> bool:
	return state.get("connectionIntent", false) or state.get("runState", "") in ["connected", "connecting", "recovering"]

func _mode(value: String, enabled: bool) -> void:
	command.emit("mode", {"mode":value, "tun":enabled})

func render(value: Dictionary, node_name: String) -> void:
	state = value
	var running: String = state.get("runState", "disconnected")
	var connected := running == "connected"
	var pending: Dictionary = state.get("commandPending", {})
	var switching: bool = state.get("selectionPending", false) and _can_stop()
	var changing: bool = running == "connecting" or switching or pending.has("mode")
	var box := get_theme_stylebox("panel") as StyleBoxFlat
	box.bg_color = Color("152725") if connected else Color("151f2b")
	box.border_color = Color("356657") if connected else Color("79613e") if running == "recovering" else Color("2d3c4c")
	glow.material.set_shader_parameter("base_color", box.bg_color)
	glow.material.set_shader_parameter("glow_color", Color("285b4c70") if connected else Color("1e3d3c80"))
	status.text = "正在切换节点" if switching else S.connection_label(running)
	dot.add_theme_color_override("font_color", S.connection_color(running))
	caption.text = "✓  TLS 验证已开启" if connected else "Hysteria 2 · 加密连接"
	if connected:
		var metrics: Dictionary = state.get("metrics", {})
		if metrics.get("delayPending", false): caption.text += " · 延迟检测中…"
		elif metrics.get("delayMs") != null: caption.text += " · " + str(int(metrics.delayMs)) + " ms"
	power.text = ("取消连接" if running == "connecting" else "断开连接") if _can_stop() else "连接"
	S.button_surface(power, S.panel(S.GOOD if connected else Color("243c40"), 9, 12, Color("a0e0c3") if connected else Color("476168")))
	power.add_theme_color_override("font_color", Color("102a24") if connected else Color("b5dace"))
	power.add_theme_color_override("icon_normal_color", Color("102a24") if connected else Color("b5dace"))
	# Connect consumes the confirmed local choice. Prevent a click in the brief
	# save/ack interval from racing the selection command and using the old node;
	# this is only button availability, never a connection/loading transition.
	var saving_choice := pending.has("node") or pending.has("subscription")
	power.disabled = pending.has("disconnect") or (not _can_stop() and (saving_choice or pending.has("connect") or state.get("selectionPending", false) or str(state.get("desiredNodeId", "")).is_empty()))
	# The selector owns the next preference; this card describes the transport
	# actually in use. A new choice must never relabel an existing connection.
	var pack = null
	for subscription in state.get("subscriptions", []):
		if subscription.id == state.get("actualSubscriptionId", ""): pack = subscription
	detail.text = (str(pack.get("templateName", "当前订阅")) + " · " + node_name) if connected and pack is Dictionary else "先选订阅，再选节点，开启这台设备的连接。"
	if running == "failed": detail.text = "代理已停止。处理提示后可以手动重连。"
	if switching: detail.text = "正在断开旧连接并连接所选节点…"
	if running == "recovering": detail.text = "节点链路中断，正在恢复原节点连接…"
	for button in [rule, global_mode]:
		var selected: bool = (button == rule) == (state.get("mode", "rule") == "rule")
		S.button_surface(button, S.panel(Color("43695d") if selected else Color.TRANSPARENT, 5, 4, Color.TRANSPARENT))
		button.add_theme_color_override("font_disabled_color", Color("d4f1e5") if selected else S.MUTED)
		button.disabled = changing or selected
	tun.set_pressed_no_signal(state.get("tunEnabled", false))
	tun.queue_redraw()
	var mobile: bool = state.get("platform", "") == "android"
	tun_label.text = "TUN · 系统 VPN" if mobile else "TUN"
	tun.disabled = mobile or changing
