extends Control
## Search, modal focus and selected subscription survive state polling. No
## network is performed here; selection/test requests are native commands.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
var state: Dictionary = {}
var panel: PanelContainer
var subtitle: Label
var search: LineEdit
var tests: Button
var grid: GridContainer
var empty: Label
var close_button: Button
var scroller: ScrollContainer
var _key := ""
var previous_focus: Control

func _ready() -> void:
	set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	var shade := ColorRect.new()
	shade.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	var material := ShaderMaterial.new()
	material.shader = preload("res://ui/modal_blur.gdshader")
	shade.material = material
	shade.gui_input.connect(func(event):
		if event is InputEventMouseButton and event.pressed: hide())
	add_child(shade)
	panel = PanelContainer.new()
	panel.add_theme_stylebox_override("panel", S.panel(Color("151e2c"), 16, 24, Color("384559")))
	# Wrapped labels may initially measure with zero width; refit after their
	# real columns settle so that the first opening cannot grow past the viewport.
	panel.minimum_size_changed.connect(_layout.call_deferred)
	add_child(panel)
	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 0)
	panel.add_child(column)
	var heading := HBoxContainer.new()
	var title := S.text("选择节点", 21)
	S.typography(title, 700)
	heading.add_child(title)
	close_button = S.button("×", hide)
	S.flat_button(close_button, 21, Color("a9b7cb"))
	close_button.custom_minimum_size = Vector2(31, 31)
	heading.add_child(close_button)
	column.add_child(heading)
	S.space(column, 5)
	subtitle = S.text("", 12, Color("92a1ba"))
	column.add_child(subtitle)
	S.space(column, 19)
	var toolbar := HBoxContainer.new()
	toolbar.add_theme_constant_override("separation", 20)
	search = LineEdit.new()
	search.placeholder_text = "搜索节点或订阅"
	search.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	search.custom_minimum_size.y = 40
	search.add_theme_font_size_override("font_size", 12)
	var search_box := S.panel(Color("0e1622"), 8, 11, Color("334055"))
	search_box.content_margin_left = 38
	search.add_theme_stylebox_override("normal", search_box)
	var search_icon := S.icon("Search", 16, Color("8193af"))
	search_icon.position = Vector2(12, 12)
	search.add_child(search_icon)
	search.text_changed.connect(func(_value): _key = ""; render(state))
	toolbar.add_child(search)
	tests = S.button("全部测速", func(): command.emit("cancelTest" if state.get("nodeTests", {}).get("pending", false) else "testNodes", {}))
	S.flat_button(tests, 11)
	toolbar.add_child(tests)
	column.add_child(toolbar)
	S.space(column, 18)
	scroller = ScrollContainer.new()
	scroller.horizontal_scroll_mode = ScrollContainer.SCROLL_MODE_DISABLED
	scroller.size_flags_vertical = Control.SIZE_EXPAND_FILL
	column.add_child(scroller)
	grid = GridContainer.new()
	grid.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	grid.add_theme_constant_override("h_separation", 12)
	grid.add_theme_constant_override("v_separation", 12)
	scroller.add_child(grid)
	empty = S.text("", 12, S.MUTED)
	column.add_child(empty)
	S.space(column, 17)
	column.add_child(S.text("已连接时自动切换节点；连接成功后显示延迟，列表测速需手动触发。", 11, Color("8d9db5")))
	resized.connect(_layout)
	visibility_changed.connect(_visibility_changed)
	hide()

func _layout() -> void:
	if grid == null: return
	var width := minf(820, size.x - (32 if size.x <= 700 else 48))
	grid.columns = 1 if size.x <= 430 else 2 if size.x <= 700 else 3
	panel.size.x = width
	var padding := 18 if size.x <= 700 else 24
	var box := panel.get_theme_stylebox("panel") as StyleBoxFlat
	if box.content_margin_left != padding: box.set_content_margin_all(padding)
	var cell_width := floorf((width - 2 * padding - 10 - (grid.columns - 1) * 12) / grid.columns)
	for button in grid.get_children(): button.custom_minimum_size.x = cell_width
	var rows := maxi(1, ceili(float(grid.get_child_count()) / grid.columns))
	var target := 222.0 + rows * 110 + (rows - 1) * 12
	panel.size.y = minf(target, minf(570, size.y - 64))
	panel.position = Vector2((size.x - (8 if size.x > 740 else 0) - panel.size.x) / 2, maxf(18, (size.y - panel.size.y) / 2 + 8))

func open(value: Dictionary) -> void:
	previous_focus = get_viewport().gui_get_focus_owner()
	show()
	render(value)
	_layout()
	search.grab_focus()

func _visibility_changed() -> void:
	if visible: return
	if is_instance_valid(previous_focus): previous_focus.grab_focus()

func render(value: Dictionary) -> void:
	state = value
	# Only catalog names/eligibility affect node rows. Metering counters and
	# unrelated account revisions must not destroy focused buttons every poll.
	var catalog := []
	for item in state.get("subscriptions", []):
		catalog.append([item.id, item.get("templateName", ""), S.valid_subscription(item)])
	var key := JSON.stringify([state.get("nodeChoices", []), catalog, state.get("nodeTests", {}), state.get("desiredNodeId", ""), state.get("desiredSubscriptionId", ""), search.text])
	if key == _key: return
	_key = key
	S.clear_children(grid)
	var subscriptions := {}
	for item in state.get("subscriptions", []):
		if S.valid_subscription(item): subscriptions[item.id] = item
	var total := 0
	var ready := 0
	for choice in state.get("nodeChoices", []):
		if not subscriptions.has(choice.subscriptionId): continue
		total += 1
		if choice.get("ready", false): ready += 1
		var subscription: Dictionary = subscriptions[choice.subscriptionId]
		var words: String = str(choice.name) + " " + str(subscription.templateName) + " " + str(choice.subscriptionId)
		if not search.text.strip_edges().is_empty() and not search.text.strip_edges().to_lower() in words.to_lower(): continue
		_add_choice(choice, subscription)
	subtitle.text = "%d 个可选节点 · 按订阅分别计量" % total
	tests.disabled = ready == 0
	tests.text = "取消测速" if state.get("nodeTests", {}).get("pending", false) else "全部测速"
	tests.tooltip_text = "取消正在进行的节点测速" if state.get("nodeTests", {}).get("pending", false) else "测试列表内的全部可用节点"
	empty.text = "没有匹配的节点" if grid.get_child_count() == 0 else ""
	empty.visible = grid.get_child_count() == 0
	_layout()
	_focus_loop()

func _add_choice(choice: Dictionary, subscription: Dictionary) -> void:
	var selected: bool = choice.nodeId == state.get("desiredNodeId", "") and choice.subscriptionId == state.get("desiredSubscriptionId", "")
	var params := {"nodeId":choice.nodeId, "subscriptionId":choice.subscriptionId}
	# Close before publishing the choice so the shell doesn't rebuild this
	# hidden grid inside the click callback just to remove it a moment later.
	var button := S.button("", func(): hide(); command.emit("node", params))
	button.custom_minimum_size.y = 110
	button.disabled = not choice.ready
	button.tooltip_text = "%s · %s" % [choice.name, subscription.templateName]
	S.button_surface(button, S.panel(Color("322e4b") if selected else Color("192333"), 10, 14, Color("ac9bff") if selected else Color("334054")))
	grid.add_child(button)
	var margin := S.inset(button, 14, 14, 14, 14)
	margin.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	margin.mouse_filter = Control.MOUSE_FILTER_IGNORE
	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 9)
	column.mouse_filter = Control.MOUSE_FILTER_IGNORE
	margin.add_child(column)
	var name := S.text(str(choice.name), 13, Color("e7edf9"))
	S.typography(name, 600)
	column.add_child(name)
	column.add_child(S.text("%s · #%s" % [subscription.templateName, str(choice.subscriptionId).right(6)], 10, Color("9eabc1")))
	var row := HBoxContainer.new()
	# The whole card is one button. Decorative layout must not intercept input;
	# in particular a plain Control spacer defaults to STOP and creates a dead
	# area over the lower-right of the card, even though it paints nothing.
	row.mouse_filter = Control.MOUSE_FILTER_IGNORE
	row.size_flags_vertical = Control.SIZE_EXPAND_FILL
	row.alignment = BoxContainer.ALIGNMENT_END
	column.add_child(row)
	var text := _latency(str(choice.nodeId)) if choice.ready else "暂不可用"
	var success := text.ends_with(" ms")
	row.add_child(S.badge(text, Color("88ddc9") if success else Color("98a8bf"), Color("163d38") if success else Color("293549")))
	var spring := Control.new()
	spring.mouse_filter = Control.MOUSE_FILTER_IGNORE
	spring.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	row.add_child(spring)
	if selected: row.add_child(S.icon("Check", 18, Color("b9a9ff")))

func _latency(id: String) -> String:
	for result in state.get("nodeTests", {}).get("results", []):
		if result.get("nodeId", "") != id: continue
		if result.get("status", "") == "success": return str(int(result.get("delayMs", 0))) + " ms"
		return {"waiting":"等待测试", "testing":"测试中…", "timeout":"超时", "failed":"连接失败", "direct_unavailable":"直连不可用", "certificate_error":"证书失败", "cancelled":"已取消"}.get(result.get("status", ""), "待测速")
	return "待测速"

func _focus_loop() -> void:
	var controls: Array[Control] = [close_button, search]
	if not tests.disabled: controls.append(tests)
	for button in grid.get_children():
		if not button.disabled: controls.append(button)
	for i in range(controls.size()):
		controls[i].focus_next = controls[i].get_path_to(controls[(i + 1) % controls.size()])
		controls[i].focus_previous = controls[i].get_path_to(controls[(i - 1 + controls.size()) % controls.size()])

func _unhandled_key_input(event: InputEvent) -> void:
	if visible and event.is_action_pressed("ui_cancel"):
		hide()
		get_viewport().set_input_as_handled()
