extends VBoxContainer
## Desktop reading order is identical to the Wails source. Presentation-only
## child views consume the same immutable runtime snapshot and emit commands.
signal command(method: String, params: Dictionary)
signal choose_node
const S = preload("res://ui/style.gd")
const Connection = preload("res://ui/connection_card.gd")
const Traffic = preload("res://ui/traffic_panel.gd")
const Usage = preload("res://ui/usage.gd")
const Plans = preload("res://ui/subscriptions.gd")
var state: Dictionary = {}
var node_button: Button
var node_name: Label
var node_subscription: Label
var node_meta: Label
var node_ready: Label
var node_latency: Label
var tests: Button
var connection: PanelContainer
var traffic: PanelContainer
var chart: Control
var values: Array[Label] = []
var tun: Button
var summary_title: Label
var summary_id: Label
var summary_badge: Label
var summary: Control
var usage: VBoxContainer
var empty: Label
var account: Button
var footer: Label
var refresh: Button
var node_panel: PanelContainer
var plans: VBoxContainer

func _ready() -> void:
	add_theme_constant_override("separation", 0)
	_build_node()
	S.space(self, 20)
	connection = Connection.new()
	connection.command.connect(func(method, params): command.emit(method, params))
	add_child(connection)
	tun = connection.tun
	S.space(self, 20)
	traffic = Traffic.new()
	traffic.command.connect(func(method, params): command.emit(method, params))
	add_child(traffic)
	chart = traffic.chart
	values = traffic.values
	S.space(self, 18)
	_build_summary()
	S.space(self, 18)
	var foot := HBoxContainer.new()
	footer = S.text("HQIP 未接管系统网络", 9, Color("61768d"))
	foot.add_child(footer)
	refresh = S.button("等待同步 · 刷新", func(): command.emit("refresh", {}))
	S.flat_button(refresh)
	foot.add_child(refresh)
	add_child(foot)

func _build_node() -> void:
	var node := S.card(self)
	node_panel = node.get_parent()
	node_panel.custom_minimum_size.y = 175
	var heading := HBoxContainer.new()
	heading.add_theme_constant_override("separation", 8)
	heading.add_child(S.icon("Globe2"))
	heading.add_child(S.text("选择节点", 12))
	tests = S.button("当前测速", _test_nodes)
	S.flat_button(tests, 11)
	tests.icon = load("res://assets/icons/Gauge.svg")
	tests.add_theme_constant_override("icon_max_width", 12)
	heading.add_child(tests)
	node.add_child(heading)
	S.space(node, 19)
	node_button = S.button("", func(): choose_node.emit())
	node_button.custom_minimum_size.y = 66
	S.button_surface(node_button, S.panel(Color("192630"), 9, 14, Color("34495a")))
	node.add_child(node_button)
	var inset := S.inset(node_button, 14, 14, 14, 14)
	inset.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	inset.mouse_filter = Control.MOUSE_FILTER_IGNORE
	var row := HBoxContainer.new()
	row.mouse_filter = Control.MOUSE_FILTER_IGNORE
	row.add_theme_constant_override("separation", 12)
	inset.add_child(row)
	var icon_box := PanelContainer.new()
	icon_box.add_theme_stylebox_override("panel", S.panel(Color("244044"), 8, 7, Color.TRANSPARENT))
	icon_box.mouse_filter = Control.MOUSE_FILTER_IGNORE
	icon_box.add_child(S.icon("Network", 20, Color("a4d6cd")))
	row.add_child(icon_box)
	var copy := VBoxContainer.new()
	copy.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	copy.mouse_filter = Control.MOUSE_FILTER_IGNORE
	copy.add_theme_constant_override("separation", 5)
	row.add_child(copy)
	node_name = S.text("请选择节点", 13)
	S.typography(node_name, 600)
	node_subscription = S.text("", 11, Color("94a7c0"))
	for label in [node_name, node_subscription]:
		label.autowrap_mode = TextServer.AUTOWRAP_OFF
		label.text_overrun_behavior = TextServer.OVERRUN_TRIM_ELLIPSIS
		copy.add_child(label)
	node_latency = S.text("", 12)
	node_latency.autowrap_mode = TextServer.AUTOWRAP_OFF
	node_latency.size_flags_horizontal = Control.SIZE_SHRINK_END
	row.add_child(node_latency)
	row.add_child(S.icon("ChevronRight", 17))
	S.space(node, 16)
	var meta := HBoxContainer.new()
	node_meta = S.text("", 10, Color("7f91a7"))
	meta.add_child(node_meta)
	node_ready = S.text("", 10, Color("99cdbb"))
	node_ready.autowrap_mode = TextServer.AUTOWRAP_OFF
	node_ready.size_flags_horizontal = Control.SIZE_SHRINK_END
	meta.add_child(node_ready)
	node.add_child(meta)

func _build_summary() -> void:
	var column := S.card(self)
	summary = column.get_parent()
	var title := HBoxContainer.new()
	title.add_theme_constant_override("separation", 8)
	title.add_child(S.icon("ShieldCheck"))
	summary_title = S.text("", 12)
	summary_title.autowrap_mode = TextServer.AUTOWRAP_OFF
	summary_title.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	title.add_child(summary_title)
	summary_id = S.text("", 9, Color("5e728a"))
	title.add_child(summary_id)
	summary_badge = S.badge("有效")
	title.add_child(summary_badge)
	column.add_child(title)
	S.space(column, 19)
	usage = Usage.new()
	column.add_child(usage)
	empty = S.text("已购买的流量包会自动同步到这里。", 12, S.MUTED)
	column.add_child(empty)
	plans = Plans.new()
	plans.compact = true
	plans.command.connect(func(method, params): command.emit(method, params))
	column.add_child(plans)
	account = S.button("前往网页订阅中心  ↗", func(): command.emit("account", {}))
	S.flat_button(account, 11)
	column.add_child(account)

func _test_nodes() -> void:
	var test: Dictionary = state.get("nodeTests", {})
	var params := {"subscriptionId":state.get("desiredSubscriptionId", ""), "nodeId":state.get("desiredNodeId", "")}
	var current: bool = test.get("pending", false) and test.get("nodeId", "") == params.nodeId and test.get("subscriptionId", "") == params.subscriptionId
	command.emit("cancelNodeTest" if current else "testNode", params)

func render(value: Dictionary) -> void:
	state = value
	var subscriptions := {}
	var subscription: Variant = null
	for item in state.get("subscriptions", []):
		# The selector's labels and summary share the desired identity. The
		# authorized snapshot may still describe the previous package.
		if item.id == state.get("desiredSubscriptionId", ""): subscription = item
		if S.valid_subscription(item): subscriptions[item.id] = item
	var selected: Dictionary = {}
	var actual_name := "—"
	var count := 0
	for choice in state.get("nodeChoices", []):
		if subscriptions.has(choice.subscriptionId):
			count += 1
		if choice.nodeId == state.get("desiredNodeId", "") and choice.subscriptionId == state.get("desiredSubscriptionId", ""): selected = choice
		if choice.nodeId == state.get("actualNodeId", "") and choice.subscriptionId == state.get("actualSubscriptionId", ""): actual_name = choice.name
	node_name.text = str(selected.get("name", "请选择节点"))
	node_subscription.text = str(subscription.get("templateName", "")) + " · #" + str(subscription.get("id", "")).right(6) if subscription is Dictionary else "从有效订阅中选择一个节点"
	node_meta.text = "%d 个节点选项 · %d 份有效订阅" % [count, subscriptions.size()]
	node_ready.text = "就绪" if selected.get("ready", false) else "等待选择"
	node_latency.text = ""
	for result in state.get("nodeTests", {}).get("results", []):
		if result.get("nodeId") == selected.get("nodeId"):
			node_latency.text = str(int(result.get("delayMs", 0))) + " ms" if result.get("status") == "success" else {"testing":"测速中", "waiting":"等待测速", "timeout":"超时", "failed":"失败", "certificate_error":"证书错误", "direct_unavailable":"直连不可用"}.get(result.get("status"), "")
	# Connection delay belongs to the actual pair, never an optimistic next
	# choice. Manual catalog-test results remain independently owned above.
	if state.get("runState") == "connected" and selected.get("nodeId") == state.get("actualNodeId") and selected.get("subscriptionId") == state.get("actualSubscriptionId"):
		var metrics: Dictionary = state.get("metrics", {})
		node_latency.text = "检测中…" if metrics.get("delayPending", false) else (str(int(metrics.delayMs)) + " ms" if metrics.get("delayMs") != null else "—")
	node_button.disabled = state.get("commandPending", {}).has("logout")
	var test: Dictionary = state.get("nodeTests", {})
	var current_test: bool = test.get("pending", false) and test.get("nodeId", "") == selected.get("nodeId") and test.get("subscriptionId", "") == selected.get("subscriptionId")
	var pending: Dictionary = state.get("commandPending", {})
	tests.disabled = not selected.get("ready", false) or not subscriptions.has(selected.get("subscriptionId")) or state.get("selectionPending", false) or ["testNode", "cancelNodeTest", "node", "subscription", "logout"].any(func(method): return pending.has(method))
	tests.text = "取消测速" if current_test else "当前测速"
	tests.tooltip_text = "仅测试当前选择的节点，不建立代理连接" if not selected.is_empty() else "请先选择节点"
	connection.render(state, actual_name)
	traffic.render(state)
	usage.visible = subscription is Dictionary
	plans.visible = not usage.visible and not state.get("subscriptions", []).is_empty()
	if plans.visible: plans.render(state)
	empty.visible = not usage.visible and not plans.visible
	account.visible = not usage.visible
	summary_badge.visible = usage.visible
	summary_id.visible = usage.visible
	if subscription is Dictionary:
		summary_title.text = str(subscription.get("templateName", "当前订阅"))
		summary_id.text = "#" + str(subscription.get("id", "")).right(6)
		summary_badge.text = {"active":"有效", "expired":"已到期", "exhausted":"流量用尽", "disabled":"已停用", "pending":"待生效"}.get(subscription.get("status"), "已失效")
		usage.render(subscription)
	else: summary_title.text = "已有订阅 · 请在上方选择节点" if plans.visible else "还没有订阅"
	footer.text = "●  实际连接：" + actual_name if state.get("runState") == "connected" else "●  HQIP 未接管系统网络"
	refresh.disabled = state.get("commandPending", {}).has("refresh")
	var synced := "等待同步" if state.get("lastSync") == null else S.date(str(state.lastSync)).split(" ")[-1]
	refresh.text = "同步中…" if refresh.disabled else ("账户暂未同步 · " if state.get("controlUnavailable", false) else "") + synced + " · 刷新"
