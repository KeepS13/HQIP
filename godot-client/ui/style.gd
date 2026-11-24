extends RefCounted
## Original desktop/frontend visual tokens, shared by all Godot views.
const BG := Color("0c111a")
const SIDE := Color("101722")
const PANEL := Color("131c28")
const INK := Color("e9edf4")
const MUTED := Color("91a1b8")
const ACCENT := Color("b5a9fa")
const BORDER := Color("253141")
const GOOD := Color("89cbb0")
const BAD := Color("f0a1a5")
const BRAND = preload("res://assets/hqip.svg")
const CJK_FONT = preload("res://assets/fonts/NotoSansCJKsc-Regular.otf")
static var font_cache: Dictionary = {}
const ICONS = {
	"connected": preload("res://assets/hqip-connected.svg"),
	"connecting": preload("res://assets/hqip-connecting.svg"),
	"recovering": preload("res://assets/hqip-recovering.svg"),
	"failed": preload("res://assets/hqip-failed.svg"),
	"disconnected": preload("res://assets/hqip-disconnected.svg")
}

static func version(value: Variant) -> String:
	var actual := str(value).strip_edges() if value != null else ""
	return "—" if actual.is_empty() else "v" + actual.trim_prefix("v")

static func connection_label(state: String) -> String:
	return {"connected":"已连接", "connecting":"正在连接", "recovering":"恢复中", "failed":"连接失败"}.get(state, "未连接")

static func connection_color(state: String) -> Color:
	return {"connected":GOOD, "recovering":Color("e5bf8a"), "failed":BAD, "connecting":ACCENT}.get(state, MUTED)

static func connection_icon(state: String) -> Texture2D:
	return ICONS.get(state, ICONS.disconnected)

static func panel(color: Color = PANEL, radius: int = 12, padding: int = 20, border: Color = BORDER) -> StyleBoxFlat:
	var box := StyleBoxFlat.new()
	box.bg_color = color
	box.set_corner_radius_all(radius)
	box.set_content_margin_all(padding)
	box.set_border_width_all(1)
	box.border_color = border
	if border.a == 0: box.set_border_width_all(0)
	return box

static func font(weight: int = 400, spacing: int = 0) -> Font:
	var key := str(weight) + ":" + str(spacing)
	if font_cache.has(key): return font_cache[key]
	var latin := SystemFont.new()
	latin.font_names = PackedStringArray(["Segoe UI"])
	latin.font_weight = weight
	var chinese := SystemFont.new()
	chinese.font_names = PackedStringArray(["Microsoft YaHei UI"])
	chinese.font_weight = weight
	chinese.fallbacks = [CJK_FONT]
	latin.fallbacks = [chinese, CJK_FONT]
	var variation := FontVariation.new()
	variation.base_font = CJK_FONT if OS.get_name() == "Android" else latin
	variation.spacing_glyph = spacing
	if OS.get_name() == "Android" and weight > 400: variation.variation_embolden = (weight - 400) / 300.0
	font_cache[key] = variation
	return variation

static func typography(control: Control, weight: int = 400, spacing: int = 0) -> void:
	control.add_theme_font_override("font", font(weight, spacing))

static func glow(parent: Control, base: Color, color: Color, center: Vector2 = Vector2(0.9, 0)) -> ColorRect:
	var rect := ColorRect.new()
	rect.mouse_filter = Control.MOUSE_FILTER_IGNORE
	var material := ShaderMaterial.new()
	material.shader = preload("res://ui/panel_glow.gdshader")
	material.set_shader_parameter("base_color", base)
	material.set_shader_parameter("glow_color", color)
	material.set_shader_parameter("center", center)
	material.set_shader_parameter("radius", maxf(0, (parent.get_theme_stylebox("panel") as StyleBoxFlat).corner_radius_top_left - 1))
	rect.material = material
	# A plain Control prevents PanelContainer from adding a second layout child.
	var layer := Control.new()
	layer.mouse_filter = Control.MOUSE_FILTER_IGNORE
	layer.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	layer.show_behind_parent = false
	parent.add_child(layer)
	parent.move_child(layer, 0)
	layer.add_child(rect)
	var fit := func():
		rect.position = Vector2.ONE - layer.position
		rect.size = parent.size - Vector2(2, 2)
		material.set_shader_parameter("dimensions", rect.size)
	parent.resized.connect(fit)
	layer.item_rect_changed.connect(fit)
	fit.call()
	return rect

static func theme() -> Theme:
	var result := Theme.new()
	var font := SystemFont.new()
	font.font_names = PackedStringArray(["Segoe UI", "Microsoft YaHei UI", "Noto Sans CJK SC", "sans-serif"])
	# Android vendors expose inconsistent CJK fallback coverage, including fullwidth
	# punctuation. Bundle the licensed font so account errors remain readable offline.
	font.fallbacks = [CJK_FONT]
	result.default_font = font(400)
	result.default_font_size = 12
	result.set_color("font_color", "Label", INK)
	for control in ["Button", "OptionButton", "CheckButton"]:
		result.set_color("font_color", control, INK)
		result.set_color("font_hover_color", control, ACCENT)
		result.set_color("font_pressed_color", control, INK)
		result.set_color("font_disabled_color", control, Color("64778e"))
		result.set_stylebox("normal", control, panel(Color("192431"), 8, 10))
		result.set_stylebox("hover", control, panel(Color("242c43"), 8, 10, ACCENT))
		result.set_stylebox("pressed", control, panel(Color("322e4b"), 8, 10, ACCENT))
		result.set_stylebox("hover_pressed", control, panel(Color("322e4b"), 8, 10, ACCENT))
		result.set_stylebox("disabled", control, panel(PANEL, 8, 10))
		result.set_stylebox("focus", control, panel(Color.TRANSPARENT, 8, 10, ACCENT))
	result.set_stylebox("normal", "LineEdit", panel(Color("0e1622"), 8, 12))
	result.set_stylebox("focus", "LineEdit", panel(Color.TRANSPARENT, 8, 12, ACCENT))
	result.set_color("font_color", "LineEdit", INK)
	result.set_color("font_placeholder_color", "LineEdit", MUTED)
	result.set_color("font_color", "PopupMenu", INK)
	result.set_stylebox("panel", "PopupMenu", panel())
	var scroll_box := panel(BG, 0, 0, Color.TRANSPARENT)
	scroll_box.content_margin_left = 4
	scroll_box.content_margin_right = 4
	result.set_stylebox("scroll", "VScrollBar", scroll_box)
	result.set_stylebox("grabber", "VScrollBar", panel(Color("303847"), 4, 0, Color.TRANSPARENT))
	result.set_stylebox("grabber_highlight", "VScrollBar", panel(Color("4a5464"), 4, 0, Color.TRANSPARENT))
	result.set_stylebox("grabber_pressed", "VScrollBar", panel(Color("4a5464"), 4, 0, Color.TRANSPARENT))
	return result

static func text(value: String, size: int = 14, color: Color = INK) -> Label:
	var label := Label.new()
	label.text = value
	label.add_theme_font_size_override("font_size", size)
	label.add_theme_color_override("font_color", color)
	label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	label.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	return label

static func button(value: String, action: Callable, primary: bool = false) -> Button:
	var result := Button.new()
	result.text = value
	result.custom_minimum_size.y = 32
	result.pressed.connect(action)
	result.mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
	if primary:
		button_surface(result, panel(ACCENT, 8, 12, ACCENT))
		result.add_theme_color_override("font_color", Color("211c37"))
		result.add_theme_color_override("font_hover_color", Color("211c37"))
		result.add_theme_color_override("font_pressed_color", Color("211c37"))
	return result

## Every state owns identical content margins. Inherited hover/pressed padding
## must never resize a custom button. Reuse overrides when polling changes no
## surface properties, so state snapshots do not keep invalidating layout.
static func button_surface(button: Button, base: StyleBoxFlat, flat: bool = false) -> void:
	var key := hash([base.bg_color, base.border_color, base.get_minimum_size(), base.corner_radius_top_left, base.content_margin_left, base.content_margin_top, flat])
	if button.get_meta("surface_key", 0) == key: return
	button.set_meta("surface_key", key)
	for state in ["normal", "hover", "pressed", "hover_pressed", "disabled", "focus"]:
		var box := base.duplicate() as StyleBoxFlat
		if state in ["hover", "pressed", "hover_pressed"] and not flat:
			box.bg_color = base.bg_color.lerp(Color("4a4166"), 0.3 if state == "hover" else 0.5)
		if state == "focus":
			box.bg_color = Color.TRANSPARENT
			box.border_color = ACCENT
			box.set_border_width_all(1)
		button.add_theme_stylebox_override(state, box)

static func card(parent: Node, title: String = "") -> VBoxContainer:
	var frame := PanelContainer.new()
	frame.add_theme_stylebox_override("panel", panel())
	frame.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	parent.add_child(frame)
	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 0)
	frame.add_child(column)
	if title != "": column.add_child(text(title, 12))
	return column

static func logo(edge: int = 40) -> TextureRect:
	var icon := TextureRect.new()
	icon.texture = BRAND
	icon.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
	icon.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
	icon.custom_minimum_size = Vector2(edge, edge)
	return icon

static func bytes(value: Variant) -> String:
	var amount := maxf(0, float(value))
	for unit in ["B", "KB", "MB", "GB", "TB"]:
		if amount < 1000 or unit == "TB": return ("%.0f " if unit == "B" else "%.1f " if unit in ["KB", "MB"] else "%.2f ") % amount + unit
		amount /= 1000
	return "0 B"

## The following primitives use original CSS logical pixels. Mobile layouts
## enlarge hit areas explicitly instead of inflating every desktop control.
static func space(parent: Node, height: float) -> Control:
	var gap := Control.new()
	gap.custom_minimum_size.y = height
	gap.mouse_filter = Control.MOUSE_FILTER_IGNORE
	parent.add_child(gap)
	return gap

static func inset(parent: Node, left: int, top: int, right: int, bottom: int) -> MarginContainer:
	var margin := MarginContainer.new()
	for pair in [["left", left], ["top", top], ["right", right], ["bottom", bottom]]:
		margin.add_theme_constant_override("margin_" + pair[0], pair[1])
	margin.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	parent.add_child(margin)
	return margin

static func flat_button(button: Button, size: int = 10, color: Color = Color("b8b1ff")) -> void:
	button.custom_minimum_size.y = 0
	button.add_theme_font_size_override("font_size", size)
	button.add_theme_color_override("font_color", color)
	button_surface(button, panel(Color.TRANSPARENT, 5, 0, Color.TRANSPARENT), true)

static func line(parent: Node, color: Color = Color("293345")) -> ColorRect:
	var divider := ColorRect.new()
	divider.color = color
	divider.custom_minimum_size.y = 1
	divider.mouse_filter = Control.MOUSE_FILTER_IGNORE
	parent.add_child(divider)
	return divider

static func badge(value: String, color: Color = Color("a8b7c6"), background: Color = Color("28343f")) -> Label:
	var label := text(value, 9, color)
	label.autowrap_mode = TextServer.AUTOWRAP_OFF
	label.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	label.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	var box := panel(background, 5, 4, Color.TRANSPARENT)
	box.content_margin_left = 7
	box.content_margin_right = 7
	label.add_theme_stylebox_override("normal", box)
	return label

static func icon(name: String, edge: int = 17, color: Color = Color("91a7bb")) -> TextureRect:
	var result := TextureRect.new()
	result.texture = load("res://assets/icons/" + name + ".svg")
	result.modulate = color
	result.expand_mode = TextureRect.EXPAND_IGNORE_SIZE
	result.stretch_mode = TextureRect.STRETCH_KEEP_ASPECT_CENTERED
	result.custom_minimum_size = Vector2(edge, edge)
	result.size_flags_vertical = Control.SIZE_SHRINK_CENTER
	result.mouse_filter = Control.MOUSE_FILTER_IGNORE
	return result

static func date(value: String) -> String:
	if value.is_empty(): return "—"
	var seconds := unix_timestamp(value) + int(Time.get_time_zone_from_system().get("bias", 0)) * 60
	var parts := Time.get_datetime_dict_from_unix_time(seconds)
	return "%d/%d/%d %02d:%02d:%02d" % [parts.year, parts.month, parts.day, parts.hour, parts.minute, parts.second]

static func valid_subscription(value: Dictionary) -> bool:
	var quota = value.get("quotaBytes")
	return value.get("status", "") == "active" and (quota == null or float(value.get("usedBytes", 0)) < float(quota)) and unix_timestamp(str(value.get("expiresAt", ""))) > Time.get_unix_time_from_system()

static func unix_timestamp(value: String) -> int:
	# Go emits RFC3339 with the device's UTC offset and optional nanoseconds.
	# Godot's datetime parser ignores timezone suffixes; subtract the offset
	# explicitly so connection duration and expiry agree across device timezones.
	if value.length() < 20: return 0
	var offset_seconds := 0
	if not value.ends_with("Z"):
		var zone := value.right(6)
		if zone[0] not in ["+", "-"] or zone[3] != ":": return 0
		var hours := zone.substr(1, 2)
		var minutes := zone.substr(4, 2)
		if not hours.is_valid_int() or not minutes.is_valid_int() or int(hours) > 23 or int(minutes) > 59: return 0
		offset_seconds = (int(hours) * 3600 + int(minutes) * 60) * (1 if zone[0] == "+" else -1)
	return Time.get_unix_time_from_datetime_string(value.substr(0, 19)) - offset_seconds

static func clear_children(parent: Node) -> void:
	for child in parent.get_children():
		parent.remove_child(child)
		child.queue_free()
