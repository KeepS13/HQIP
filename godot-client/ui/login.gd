extends VBoxContainer
## Browser login retains edited address/expansion across native state polling.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
var address: LineEdit
var submit: Button
var cancel: Button
var hint: Label
var protection: Label
var service: VBoxContainer
var address_edited := false
var current: Label
var reset: Button

func _ready() -> void:
	add_theme_constant_override("separation", 0)
	var center := CenterContainer.new()
	center.add_child(S.logo(60))
	add_child(center)
	S.space(self, 24)
	var title := S.text("订阅随账户，连接由你选择", 22)
	S.typography(title, 500)
	title.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	title.custom_minimum_size.y = 33
	add_child(title)
	S.space(self, 26)
	var form := VBoxContainer.new()
	form.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
	form.custom_minimum_size.x = 420
	form.add_theme_constant_override("separation", 0)
	add_child(form)
	var button_row := CenterContainer.new()
	submit = S.button("登录 / 注册    ↗", func(): command.emit("login", {"server": address.text.strip_edges()}), true)
	submit.custom_minimum_size = Vector2(152, 44)
	submit.add_theme_font_size_override("font_size", 14)
	button_row.add_child(submit)
	form.add_child(button_row)
	S.space(form, 18)
	S.line(form)
	S.space(form, 14)
	var toggle := S.button("", func(): service.visible = not service.visible)
	S.flat_button(toggle)
	toggle.custom_minimum_size.y = 18
	form.add_child(toggle)
	var row := HBoxContainer.new()
	row.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	row.mouse_filter = Control.MOUSE_FILTER_IGNORE
	toggle.add_child(row)
	row.add_child(S.text("服务地址", 11, Color("a5b4c9")))
	current = S.text("", 10, Color("7f91a8"))
	current.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	current.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
	current.autowrap_mode = TextServer.AUTOWRAP_OFF
	row.add_child(current)
	row.add_child(S.icon("ChevronDown", 14))
	service = VBoxContainer.new()
	service.add_theme_constant_override("separation", 9)
	form.add_child(service)
	S.space(service, 7)
	service.add_child(S.text("账户服务地址", 11, Color("a8b7c8")))
	address = LineEdit.new()
	address.text = "https://hqip.sharevps.online"
	address.custom_minimum_size.y = 42
	address.text_changed.connect(func(_value): address_edited = true; current.text = address.text)
	service.add_child(address)
	service.add_child(S.text("填写 HTTPS 根地址，登录成功后保存。", 10, Color("8495ad")))
	reset = S.button("恢复默认地址", func(): address.text = "https://hqip.sharevps.online"; current.text = address.text; address_edited = true)
	S.flat_button(reset, 11)
	reset.size_flags_horizontal = Control.SIZE_SHRINK_END
	service.add_child(reset)
	service.hide()
	S.space(self, 20)
	hint = S.text("", 14, Color("a5b4c9"))
	hint.custom_minimum_size.y = 25
	hint.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	add_child(hint)
	cancel = S.button("取消本次登录", func(): command.emit("cancelLogin", {}))
	S.flat_button(cancel, 13)
	cancel.custom_minimum_size.y = 32
	add_child(cancel)
	S.space(self, 28)
	var protection_row := HBoxContainer.new()
	protection_row.size_flags_horizontal = Control.SIZE_SHRINK_CENTER
	protection_row.add_theme_constant_override("separation", 8)
	protection_row.add_child(S.icon("ShieldCheck", 16, Color("8faaa9")))
	protection = S.text("", 12, Color("8faaa9"))
	protection.autowrap_mode = TextServer.AUTOWRAP_OFF
	protection.add_theme_constant_override("line_spacing", 5)
	protection.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	protection_row.add_child(protection)
	add_child(protection_row)
	resized.connect(func(): form.custom_minimum_size.x = minf(420, size.x))

func render(state: Dictionary) -> void:
	if not address_edited and not str(state.get("serverUrl", "")).is_empty(): address.text = state.serverUrl
	current.text = address.text
	var pending: bool = state.get("loginPending", false) or state.get("commandPending", {}).has("login")
	submit.disabled = pending
	address.editable = not pending
	reset.disabled = pending or address.text == "https://hqip.sharevps.online"
	submit.text = "等待浏览器授权…" if pending else "登录 / 注册    ↗"
	cancel.visible = pending
	hint.text = "请在浏览器中完成登录，完成后返回此处。" if pending else "使用 HQIP 账户，在浏览器中安全登录。"
	protection.text = "登录凭据由 %s 加密保护\n客户端不保存密码" % ("Android Keystore" if state.get("platform", "") == "android" else "Windows")
