extends PanelContainer
## Read-only subscription card following the website's hierarchy. All counters
## come from the runtime snapshot; no account action or local-choice badge lives
## here. Unknown billing-period fields are never inferred from expiry dates.
const S = preload("res://ui/style.gd")
var snapshot: Dictionary = {}
var title: Label
var identity: Label
var status: Label
var remaining: Label
var track: ProgressBar
var used: Label
var quota: Label
var reserved: Label
var expires: Label
var bandwidth: Label
var occupied: Label

func _ready() -> void:
	size_flags_horizontal = Control.SIZE_EXPAND_FILL
	add_theme_stylebox_override("panel", S.panel(S.PANEL, 14, 22))
	var body := VBoxContainer.new()
	body.add_theme_constant_override("separation", 0)
	add_child(body)
	var head := HBoxContainer.new()
	head.add_theme_constant_override("separation", 12)
	var symbol := PanelContainer.new()
	symbol.add_theme_stylebox_override("panel", S.panel(Color("26243d"), 10, 10, Color("3d3653")))
	symbol.add_child(S.icon("ShieldCheck", 22, S.ACCENT))
	head.add_child(symbol)
	var spacer := Control.new()
	spacer.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	head.add_child(spacer)
	status = S.badge("")
	head.add_child(status)
	body.add_child(head)
	S.space(body, 20)
	title = S.text("", 20)
	S.typography(title, 650)
	body.add_child(title)
	S.space(body, 7)
	identity = S.text("", 10, S.MUTED)
	body.add_child(identity)
	S.space(body, 22)
	remaining = S.text("", 28, Color("ded8ff"))
	S.typography(remaining, 600)
	body.add_child(remaining)
	body.add_child(S.text("剩余流量", 11, S.MUTED))
	S.space(body, 14)
	track = ProgressBar.new()
	track.custom_minimum_size.y = 5
	track.show_percentage = false
	track.add_theme_stylebox_override("background", S.panel(Color("293348"), 6, 0, Color.TRANSPARENT))
	track.add_theme_stylebox_override("fill", S.panel(S.ACCENT, 6, 0, Color.TRANSPARENT))
	body.add_child(track)
	S.space(body, 10)
	var counters := HBoxContainer.new()
	counters.add_theme_constant_override("separation", 10)
	used = S.text("", 11, S.MUTED)
	quota = S.text("", 11, S.MUTED)
	quota.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	counters.add_child(used)
	counters.add_child(quota)
	body.add_child(counters)
	S.space(body, 10)
	reserved = S.text("", 10, S.MUTED)
	body.add_child(reserved)
	S.space(body, 20)
	S.line(body)
	S.space(body, 18)
	expires = _fact(body, "到期时间")
	bandwidth = _fact(body, "本包共享带宽")
	occupied = _fact(body, "同时在线")

func _fact(parent: VBoxContainer, caption: String) -> Label:
	var row := HBoxContainer.new()
	row.add_theme_constant_override("separation", 12)
	var label := S.text(caption, 11, S.MUTED)
	label.size_flags_stretch_ratio = 0.8
	row.add_child(label)
	var value := S.text("", 11)
	value.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	row.add_child(value)
	parent.add_child(row)
	S.space(parent, 12)
	return value

func render(item: Dictionary) -> void:
	# The effective status also accounts for expiry between account refreshes.
	var status_code: String = str(item.get("status", ""))
	if status_code == "active":
		if item.get("quotaBytes") != null and float(item.get("usedBytes", 0)) >= float(item.quotaBytes): status_code = "exhausted"
		elif not S.valid_subscription(item): status_code = "expired"
	var key := item.merged({"_effectiveStatus":status_code})
	if snapshot == key: return
	snapshot = key.duplicate(true)
	title.text = str(item.get("templateName", "订阅"))
	identity.text = "#%s · 创建于 %s" % [str(item.id).right(8), S.date(str(item.get("createdAt", "")))]
	status.text = {"active":"有效", "expired":"已到期", "exhausted":"流量用尽", "disabled":"已停用", "pending":"待生效", "removed":"已移除", "revoked":"已撤销"}.get(status_code, "待同步")
	var active := status_code == "active"
	status.add_theme_color_override("font_color", S.GOOD if active else S.MUTED)
	status.add_theme_stylebox_override("normal", S.panel(Color("203b34") if active else Color("283040"), 4, 5, Color.TRANSPARENT))
	var limit = item.get("quotaBytes")
	var amount := float(item.get("usedBytes", 0))
	remaining.text = "不限量" if limit == null else S.bytes(maxf(0, float(limit) - amount))
	track.value = 0 if limit == null else clampf(amount / maxf(1, float(limit)) * 100, 0, 100)
	used.text = "已用 " + S.bytes(amount)
	quota.text = "共 " + ("不限量" if limit == null else S.bytes(limit))
	reserved.text = "待结算 %s，与已用流量分别记录。" % S.bytes(item.get("reservedBytes", 0))
	expires.text = "待开通" if status_code == "pending" else S.date(str(item.get("expiresAt", "")))
	bandwidth.text = "不限速" if item.get("bandwidthMbps") == null else str(int(item.bandwidthMbps)) + " Mbps"
	occupied.text = "%d / %d" % [int(item.get("occupied", 0)), int(item.get("maxConnections", 0))]
