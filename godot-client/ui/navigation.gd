extends PanelContainer
## Single navigation tree for desktop and drawer. Runtime versions are kept in
## metadata and settings; the original sidebar contains only the core version.
signal selected(section: String)
signal help_requested
signal close_requested
const S = preload("res://ui/style.gd")
const Chart = preload("res://ui/traffic_chart.gd")
var buttons: Array[Button] = []
var close_button: Button
var version: Label
var core: Label
var connection: Label
var mini: VBoxContainer
var chart: Control
var rates: Label
var metric_values: Array[Label] = []
var brand: TextureRect
var help_button: Button
var nav_dot: Label

func _ready() -> void:
	var box := S.panel(S.SIDE, 0, 18, Color("222c3a"))
	box.set_border_width_all(0)
	box.border_width_right = 1
	box.content_margin_top = 40
	box.content_margin_bottom = 20
	add_theme_stylebox_override("panel", box)
	var scroll := ScrollContainer.new()
	scroll.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
	add_child(scroll)
	var column := VBoxContainer.new()
	column.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	column.size_flags_vertical = Control.SIZE_EXPAND_FILL
	column.add_theme_constant_override("separation", 0)
	scroll.add_child(column)
	var branding := S.inset(column, 10, 0, 0, 42)
	var header := HBoxContainer.new()
	header.custom_minimum_size.y = 44
	header.add_theme_constant_override("separation", 11)
	branding.add_child(header)
	brand = S.logo(39)
	brand.mouse_filter = Control.MOUSE_FILTER_STOP
	brand.mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
	brand.tooltip_text = "点击收起左侧菜单"
	brand.gui_input.connect(func(event):
		if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed: close_requested.emit())
	brand.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	header.add_child(brand)
	var name := VBoxContainer.new()
	name.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	name.add_theme_constant_override("separation", 2)
	var title := S.text("HQIP", 24)
	S.typography(title, 700, 2)
	name.add_child(title)
	var slogan := S.text("YOUR CONNECTION", 7, Color("80919e"))
	S.typography(slogan, 500, 1)
	name.add_child(slogan)
	header.add_child(name)
	close_button = S.button("收起菜单", func(): close_requested.emit())
	column.add_child(close_button)
	var workspace := S.inset(column, 14, 0, 0, 15)
	workspace.add_child(S.text("我的空间", 10, Color("64778e")))
	for entry in [["network", "网络仪表盘", "Gauge"], ["subscriptions", "我的订阅", "Layers"], ["settings", "连接设置", "SlidersHorizontal"]]:
		var key: String = entry[0]
		var button := S.button(entry[1], func(): selected.emit(key))
		button.custom_minimum_size.y = 44
		button.alignment = HORIZONTAL_ALIGNMENT_LEFT
		button.icon = load("res://assets/icons/" + entry[2] + ".svg")
		button.add_theme_constant_override("icon_max_width", 18)
		button.add_theme_constant_override("h_separation", 11)
		button.add_theme_font_size_override("font_size", 12)
		button.set_meta("section", key)
		column.add_child(button)
		buttons.append(button)
		if key == "network":
			nav_dot = S.text("●", 9, S.GOOD)
			nav_dot.set_anchors_and_offsets_preset(Control.PRESET_CENTER_RIGHT)
			nav_dot.position.x = -19
			button.add_child(nav_dot)
		S.space(column, 7)
	var spring := Control.new()
	spring.size_flags_vertical = Control.SIZE_EXPAND_FILL
	column.add_child(spring)
	var mini_margin := S.inset(column, 6, 14, 6, 4)
	mini = VBoxContainer.new()
	mini.add_theme_constant_override("separation", 0)
	mini_margin.add_child(mini)
	S.line(mini, Color("273141"))
	S.space(mini, 12)
	chart = Chart.new()
	chart.mini = true
	mini.add_child(chart)
	chart.custom_minimum_size.y = 50
	S.space(mini, 9)
	for entry in [["↑ 上传", "b8a7ff"], ["↓ 下载", "79d5bf"], ["▱ 内核内存", "91a1b8"]]:
		var row := HBoxContainer.new()
		row.add_child(S.text(entry[0], 10, Color(entry[1])))
		var value := S.text("—", 11, Color(entry[1]))
		value.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
		row.add_child(value)
		metric_values.append(value)
		mini.add_child(row)
		if metric_values.size() < 3: S.space(mini, 7)
	S.space(mini, 3)
	S.line(mini, Color("273141"))
	var core_margin := S.inset(column, 7, 12, 7, 12)
	var core_row := HBoxContainer.new()
	core_row.add_child(S.text("●  sing-box", 10, Color("7d8b9e")))
	core = S.text("sing-box  —", 10, Color("a7b3c5"))
	core.hide()
	core_row.add_child(core)
	var core_display := S.text("—", 10, Color("a7b3c5"))
	core_display.name = "CoreVersion"
	core_display.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	core_row.add_child(core_display)
	core_margin.add_child(core_row)
	version = S.text("")
	version.hide()
	column.add_child(version)
	connection = S.text("")
	connection.hide()
	column.add_child(connection)
	rates = S.text("")
	rates.hide()
	column.add_child(rates)
	help_button = S.button("使用帮助", func(): help_requested.emit())
	help_button.custom_minimum_size.y = 40
	help_button.add_theme_font_size_override("font_size", 11)
	S.button_surface(help_button, S.panel(Color.TRANSPARENT, 8, 11, Color("273344")))
	column.add_child(help_button)
	var help_icon := S.icon("CircleHelp", 17)
	help_icon.position = Vector2(11, 11)
	help_button.add_child(help_icon)

func render(state: Dictionary, section: String) -> void:
	for button in buttons:
		var active: bool = button.get_meta("section") == section
		S.button_surface(button, S.panel(Color("26283f") if active else Color.TRANSPARENT, 9, 13, Color.TRANSPARENT))
		button.add_theme_color_override("font_color", Color("c5c0ff") if active else Color("8f9eb3"))
		button.add_theme_color_override("icon_normal_color", Color("c5c0ff") if active else Color("8f9eb3"))
	version.text = "HQIP  " + S.version(state.get("version", ""))
	core.text = "sing-box  " + S.version(state.get("coreVersion", ""))
	find_child("CoreVersion", true, false).text = S.version(state.get("coreVersion", ""))
	var run: String = state.get("runState", "disconnected")
	connection.text = S.connection_label(run)
	brand.texture = S.connection_icon(run)
	nav_dot.add_theme_color_override("font_color", S.GOOD if run == "connected" else Color("66748a"))
	mini.get_parent().visible = state.get("loggedIn", false)
	var metrics: Dictionary = state.get("metrics", {})
	var live: bool = run == "connected" and metrics.get("available", false) and not metrics.get("stale", false)
	chart.points = metrics.get("history", []) if live else []
	chart.queue_redraw()
	metric_values[0].text = S.bytes(metrics.get("uploadRate", 0)) + "/s" if live else "—"
	metric_values[1].text = S.bytes(metrics.get("downloadRate", 0)) + "/s" if live else "—"
	metric_values[2].text = S.bytes(metrics.get("memoryBytes", 0)) if live and metrics.get("memoryBytes", 0) > 0 else "—"

func focus_drawer() -> void:
	var controls: Array[Button] = [close_button]
	controls.append_array(buttons)
	controls.append(help_button)
	for i in range(controls.size()):
		controls[i].focus_next = controls[i].get_path_to(controls[(i + 1) % controls.size()])
		controls[i].focus_previous = controls[i].get_path_to(controls[(i - 1 + controls.size()) % controls.size()])
	buttons[0].grab_focus()

func release_focus_loop() -> void:
	for button in [close_button, help_button] + buttons:
		button.focus_next = NodePath()
		button.focus_previous = NodePath()
