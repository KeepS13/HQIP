extends SceneTree
## Route actual pointer events through Godot hit testing. Emitting pressed
## directly cannot detect a decorative child swallowing part of a card.
const Picker = preload("res://ui/node_picker.gd")
const S = preload("res://ui/style.gd")
var failures: Array[String] = []
var commands: Array = []
var picker: Control

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	picker = Picker.new()
	picker.theme = S.theme()
	root.add_child(picker)
	picker.command.connect(func(method, params): commands.append([method, params]))
	var fixture: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://tests/visual-fixture.json"))
	fixture.nodeChoices = fixture.nodeChoices.slice(0, 2)
	var points := [Vector2(0.06, 0.1), Vector2(0.3, 0.22), Vector2(0.3, 0.48), Vector2(0.12, 0.75), Vector2(0.75, 0.75), Vector2(0.93, 0.75), Vector2(0.96, 0.93)]
	for width in [1040, 640, 360]:
		root.size = Vector2i(width, 820)
		for index in range(2): # Selected and unselected cards both own the whole rectangle.
			for point in points:
				picker.open(fixture)
				await settle()
				var button: Button = picker.grid.get_child(index)
				var rect := button.get_global_rect()
				var before := commands.size()
				await click_at(rect.position + rect.size * point)
				check(commands.size() == before + 1, "Card missed pointer at width=%d card=%d point=%s" % [width, index, point])
				if commands.size() == before + 1:
					check(commands.back() == ["node", {"nodeId":fixture.nodeChoices[index].nodeId, "subscriptionId":fixture.nodeChoices[index].subscriptionId}], "Card emitted a wrong or duplicate command")
				check(not picker.visible, "Successful card selection did not close picker")
	# Ignoring decorative children must not bypass the outer button's disabled state.
	fixture.nodeChoices[1].ready = false
	picker.open(fixture)
	await settle()
	var disabled: Button = picker.grid.get_child(1)
	var before := commands.size()
	await click_at(disabled.get_global_rect().position + disabled.size * Vector2(0.75, 0.75))
	check(commands.size() == before and picker.visible, "Disabled node became selectable")
	picker.free()
	print("PASS node card hit areas: 42 enabled points, disabled card" if failures.is_empty() else "FAIL node card hit areas")
	quit(0 if failures.is_empty() else 1)

func settle() -> void:
	for _frame in range(4): await process_frame

func click_at(position: Vector2) -> void:
	var motion := InputEventMouseMotion.new()
	motion.position = position
	root.push_input(motion)
	for pressed in [true, false]:
		var event := InputEventMouseButton.new()
		event.position = position
		event.button_index = MOUSE_BUTTON_LEFT
		event.pressed = pressed
		root.push_input(event)
		await process_frame
