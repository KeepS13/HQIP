extends Control
## Original Windows title strip. Native drag/resize preserve DPI and OS snapping;
## closing delegates to the same tray lifetime as the system close request.
var native_enabled := true
const S = preload("res://ui/style.gd")

func _ready() -> void:
	set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	mouse_filter = Control.MOUSE_FILTER_IGNORE
	if native_enabled and OS.get_name() == "Windows":
		get_window().borderless = true
		get_window().min_size = Vector2i(320, 480)
	var strip := PanelContainer.new()
	strip.set_anchors_and_offsets_preset(Control.PRESET_TOP_WIDE)
	strip.offset_bottom = 34
	strip.add_theme_stylebox_override("panel", S.panel(Color.TRANSPARENT, 0, 0, Color.TRANSPARENT))
	add_child(strip)
	var row := HBoxContainer.new()
	row.add_theme_constant_override("separation", 2)
	strip.add_child(row)
	var drag := Control.new()
	drag.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	drag.gui_input.connect(func(event):
		if not native_enabled: return
		if event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
			if event.double_click: _maximize()
			else: DisplayServer.window_start_drag())
	row.add_child(drag)
	for item in [["Minus", "最小化", _minimize], ["Square", "最大化或还原", _maximize], ["X", "关闭到托盘", _close]]:
		var button := S.button("", item[2])
		button.icon = load("res://assets/icons/" + item[0] + ".svg")
		button.add_theme_constant_override("icon_max_width", 12 if item[0] == "Square" else 15)
		button.add_theme_color_override("icon_normal_color", Color("8e9bb2"))
		button.size_flags_vertical = Control.SIZE_SHRINK_CENTER
		button.tooltip_text = item[1]
		button.custom_minimum_size = Vector2(36, 28)
		S.button_surface(button, S.panel(S.BG, 4, 3, S.BG))
		button.add_theme_stylebox_override("hover", S.panel(Color("29283f"), 4, 3, Color("29283f")))
		row.add_child(button)
	var end := Control.new()
	end.custom_minimum_size.x = 5
	row.add_child(end)
	# Native edge resizing is required after removing the operating-system frame.
	for edge in range(8):
		var handle := Control.new()
		handle.set_meta("edge", edge)
		handle.gui_input.connect(func(event):
			if native_enabled and event is InputEventMouseButton and event.button_index == MOUSE_BUTTON_LEFT and event.pressed:
				DisplayServer.window_start_resize(edge))
		add_child(handle)
	resized.connect(_layout_edges)
	_layout_edges()

func _layout_edges() -> void:
	var boxes := [Rect2(0, 0, 5, 5), Rect2(5, 0, size.x - 10, 4), Rect2(size.x - 5, 0, 5, 5), Rect2(0, 5, 4, size.y - 10), Rect2(size.x - 4, 5, 4, size.y - 10), Rect2(0, size.y - 5, 5, 5), Rect2(5, size.y - 4, size.x - 10, 4), Rect2(size.x - 5, size.y - 5, 5, 5)]
	for child in get_children():
		if not child.has_meta("edge"): continue
		var box: Rect2 = boxes[child.get_meta("edge")]
		child.position = box.position
		child.size = box.size

func _minimize() -> void:
	if native_enabled: get_window().mode = Window.MODE_MINIMIZED

func _maximize() -> void:
	if native_enabled: get_window().mode = Window.MODE_WINDOWED if get_window().mode == Window.MODE_MAXIMIZED else Window.MODE_MAXIMIZED

func _close() -> void:
	if not native_enabled: return
	if get_tree().auto_accept_quit: get_tree().quit()
	else: get_window().close_requested.emit()
