extends Button
## CSS-sized switch, drawn independently of platform-provided CheckButton art.
## Owns only presentation; callers use set_pressed_no_signal for runtime state.
var compact := false

func _ready() -> void:
	toggle_mode = true
	mouse_default_cursor_shape = Control.CURSOR_POINTING_HAND
	custom_minimum_size = Vector2(28, 21) if compact else Vector2(38, 24)
	for state in ["normal", "hover", "pressed", "hover_pressed", "disabled", "focus"]:
		add_theme_stylebox_override(state, StyleBoxEmpty.new())
	focus_entered.connect(queue_redraw)
	focus_exited.connect(queue_redraw)
	toggled.connect(func(_pressed): queue_redraw())
	resized.connect(queue_redraw)

func _draw() -> void:
	var width := 28.0 if compact else 38.0
	var height := 16.0 if compact else 21.0
	var bounds := Rect2(Vector2(size.x - width, (size.y - height) / 2), Vector2(width, height))
	var track := StyleBoxFlat.new()
	track.bg_color = Color("548d78") if button_pressed else Color("293749")
	track.set_corner_radius_all(14)
	if disabled: track.bg_color.a = 0.5
	if has_focus():
		track.set_border_width_all(1)
		track.border_color = Color("b9a9ff")
	draw_style_box(track, bounds)
	var radius := 6.0 if compact else 7.5
	var x := bounds.end.x - radius - 3 if button_pressed else bounds.position.x + radius + 3
	var ink := Color("d5eddf") if button_pressed else Color("8596a9")
	if disabled: ink.a = 0.5
	draw_circle(Vector2(x, size.y / 2), radius, ink, true, -1, true)
