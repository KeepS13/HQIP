extends VBoxContainer
## Reusable subscription figures/progress/meta, matching the original Usage.
## Receives a read-only view; it never computes billing or changes selection.
const S = preload("res://ui/style.gd")
var figures: HBoxContainer
var values: Array[Label] = []
var track: ProgressBar
var meta: Label
var inactive: Label

func _ready() -> void:
	add_theme_constant_override("separation", 0)
	figures = HBoxContainer.new()
	figures.add_theme_constant_override("separation", 16)
	add_child(figures)
	for title in ["可用流量", "本包共享带宽", "同时在线"]:
		var cell := VBoxContainer.new()
		cell.size_flags_horizontal = Control.SIZE_EXPAND_FILL
		cell.size_flags_stretch_ratio = 1.2 if values.is_empty() else 1.0 if values.size() == 1 else 0.7
		cell.add_theme_constant_override("separation", 9)
		cell.add_child(S.text(title, 10, Color("91a1ba")))
		var value := S.text("—", 18, Color("d8d1ff"))
		S.typography(value, 500)
		cell.add_child(value)
		values.append(value)
		figures.add_child(cell)
	inactive = S.text("已失效", 10, S.MUTED)
	add_child(inactive)
	var track_margin := S.inset(self, 0, 20, 0, 0)
	track = ProgressBar.new()
	track.custom_minimum_size.y = 5
	track.show_percentage = false
	track.add_theme_stylebox_override("background", S.panel(Color("293348"), 6, 0, Color.TRANSPARENT))
	track.add_theme_stylebox_override("fill", S.panel(S.ACCENT, 6, 0, Color.TRANSPARENT))
	track_margin.add_child(track)
	S.space(self, 17)
	meta = S.text("", 11, Color("8a9db5"))
	meta.add_theme_constant_override("line_spacing", 5)
	add_child(meta)
	resized.connect(_layout)

func _layout() -> void:
	figures.add_theme_constant_override("separation", 8 if size.x < 450 else 16)
	for value in values: value.add_theme_font_size_override("font_size", 12 if size.x < 280 else 16 if size.x < 450 else 18 if get_viewport_rect().size.x <= 1050 else 20)

func render(item: Dictionary) -> void:
	var active := S.valid_subscription(item)
	figures.visible = active
	inactive.visible = not active
	inactive.text = "待生效" if item.get("status") == "pending" else "已失效"
	var quota = item.get("quotaBytes")
	values[0].text = "无限" if quota == null else S.bytes(maxf(0, float(quota) - float(item.get("usedBytes", 0))))
	values[1].text = "无限" if item.get("bandwidthMbps") == null else str(int(item.bandwidthMbps)) + " Mbps"
	values[2].text = "%d / %d" % [int(item.get("occupied", 0)), int(item.get("maxConnections", 0))]
	track.get_parent().visible = active and quota != null
	track.value = 0 if quota == null else minf(100, float(item.get("usedBytes", 0)) / maxf(1, float(quota)) * 100)
	meta.text = "已确认使用 %s · 待结算 %s\n到期 %s · 一次性额度，不按月重置" % [S.bytes(item.get("usedBytes", 0)), S.bytes(item.get("reservedBytes", 0)), S.date(str(item.get("expiresAt", "")))]
