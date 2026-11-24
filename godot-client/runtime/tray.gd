extends Node
## The native Windows tray keeps the same close-to-tray behavior as Wails.
## Explicit Quit ends the private pipe; the companion then restores networking.
const S = preload("res://ui/style.gd")
signal command(method: String, params: Dictionary)
var indicator := -1
var menu: RID
var state: Dictionary = {}
var _key := ""

func _ready() -> void:
	if OS.get_name() != "Windows" or not DisplayServer.has_feature(DisplayServer.FEATURE_STATUS_INDICATOR): return
	if not NativeMenu.has_feature(NativeMenu.FEATURE_POPUP_MENU): return
	indicator = DisplayServer.create_status_indicator(S.BRAND, "HQIP · 未连接", _activate)
	if indicator < 0: return
	menu = NativeMenu.create_menu()
	NativeMenu.add_item(menu, "打开 HQIP", func(_tag): _show())
	NativeMenu.add_item(menu, "连接", func(_tag): command.emit("disconnect" if state.get("connectionIntent", false) else "connect", {}))
	NativeMenu.add_item(menu, "规则模式", func(_tag): command.emit("mode", {"mode":"rule", "tun":state.get("tunEnabled", false)}))
	NativeMenu.add_item(menu, "全局模式", func(_tag): command.emit("mode", {"mode":"global", "tun":state.get("tunEnabled", false)}))
	NativeMenu.add_item(menu, "TUN 虚拟网卡", func(_tag): command.emit("mode", {"mode":state.get("mode", "rule"), "tun":not state.get("tunEnabled", false)}))
	NativeMenu.add_item(menu, "退出并恢复网络", func(_tag): get_tree().quit())
	DisplayServer.status_indicator_set_menu(indicator, menu)
	get_tree().auto_accept_quit = false
	get_window().close_requested.connect(func(): get_window().hide())

func render(value: Dictionary) -> void:
	state = value
	if indicator < 0: return
	var run: String = state.get("runState", "disconnected")
	var key := JSON.stringify([run, state.get("connectionIntent", false), state.get("mode", "rule"), state.get("tunEnabled", false), state.get("loggedIn", false), state.get("selectionPending", false), state.get("desiredNodeId", "")])
	if key == _key: return
	_key = key
	DisplayServer.status_indicator_set_icon(indicator, S.connection_icon(run))
	DisplayServer.status_indicator_set_tooltip(indicator, "HQIP · " + S.connection_label(run))
	var intent: bool = state.get("connectionIntent", false)
	NativeMenu.set_item_text(menu, 1, "断开连接" if intent else "连接")
	NativeMenu.set_item_disabled(menu, 1, not intent and (not state.get("loggedIn", false) or state.get("selectionPending", false) or state.get("desiredNodeId", "") == ""))
	for index in [2, 3, 4]:
		NativeMenu.set_item_disabled(menu, index, run == "connecting" or state.get("selectionPending", false))
		NativeMenu.set_item_checkable(menu, index, true)
	NativeMenu.set_item_checked(menu, 2, state.get("mode", "rule") == "rule")
	NativeMenu.set_item_checked(menu, 3, state.get("mode", "rule") == "global")
	NativeMenu.set_item_checked(menu, 4, state.get("tunEnabled", false))

func _activate(button: int, _position: Vector2i) -> void:
	if button == MOUSE_BUTTON_LEFT: _show()

func _show() -> void:
	get_window().show()
	get_window().mode = Window.MODE_WINDOWED
	get_window().grab_focus()

func _exit_tree() -> void:
	if indicator >= 0: DisplayServer.delete_status_indicator(indicator)
	if menu.is_valid(): NativeMenu.free_menu(menu)
