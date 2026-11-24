extends PanelContainer
## Layout-only metrics view. Missing samples remain unknown; native accounting
## and timestamps remain authoritative even while the latest sample is stale.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
const Chart = preload("res://ui/traffic_chart.gd")
var chart: Control
var values: Array[Label] = []
var grid: GridContainer
var session: Label
var latency: Label
var delay: Button
var metric_note: Label

func _ready() -> void:
	var box := S.panel(S.PANEL, 12, 18)
	box.content_margin_top = 15
	box.content_margin_bottom = 15
	add_theme_stylebox_override("panel", box)
	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 0)
	add_child(column)
	var heading := HBoxContainer.new()
	heading.add_child(S.text("流量统计", 12))
	for item in [["↑ 上传", "b8a7ff"], ["↓ 下载", "79d5bf"]]:
		var label := S.text(item[0], 10, Color(item[1]))
		label.autowrap_mode = TextServer.AUTOWRAP_OFF
		label.size_flags_horizontal = Control.SIZE_SHRINK_END
		heading.add_child(label)
	column.add_child(heading)
	S.space(column, 8)
	chart = Chart.new()
	column.add_child(chart)
	var labels := HBoxContainer.new()
	labels.add_child(S.text("60 秒前", 8, Color("7b8ba3")))
	var now := S.text("现在", 8, Color("7b8ba3"))
	now.horizontal_alignment = HORIZONTAL_ALIGNMENT_RIGHT
	labels.add_child(now)
	column.add_child(labels)
	S.space(column, 11)
	grid = GridContainer.new()
	grid.columns = 3
	grid.add_theme_constant_override("h_separation", 18)
	grid.add_theme_constant_override("v_separation", 4)
	column.add_child(grid)
	for title in ["↑ 上传速度", "↓ 下载速度", "应用连接", "累计上传", "累计下载", "内核内存"]:
		var cell_frame := PanelContainer.new()
		cell_frame.size_flags_horizontal = Control.SIZE_EXPAND_FILL
		var cell_box := S.panel(Color.TRANSPARENT, 0, 0, Color("2b3547"))
		cell_box.set_border_width_all(0)
		if values.size() % 3 != 0:
			cell_box.border_width_left = 1
			cell_box.content_margin_left = 12
		cell_frame.add_theme_stylebox_override("panel", cell_box)
		grid.add_child(cell_frame)
		var cell := VBoxContainer.new()
		cell.add_theme_constant_override("separation", 5)
		cell.add_child(S.text(title, 10, S.MUTED))
		var label := S.text("—", 16)
		S.typography(label, 500)
		cell.add_child(label)
		values.append(label)
		cell_frame.add_child(cell)
	values[0].add_theme_color_override("font_color", Color("c3b3ff"))
	values[1].add_theme_color_override("font_color", Color("8addc9"))
	S.space(column, 12)
	S.line(column)
	S.space(column, 8)
	var timing := HFlowContainer.new()
	timing.add_theme_constant_override("h_separation", 16)
	column.add_child(timing)
	session = S.text("连接时长 —", 10, Color("8f9fb7"))
	session.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	session.custom_minimum_size.x = 160
	timing.add_child(session)
	latency = S.text("代理延迟 —", 10, Color("8f9fb7"))
	latency.autowrap_mode = TextServer.AUTOWRAP_OFF
	latency.size_flags_horizontal = Control.SIZE_SHRINK_END
	timing.add_child(latency)
	delay = S.button("检测延迟", func(): command.emit("delay", {}))
	S.flat_button(delay)
	delay.custom_minimum_size.y = 24
	timing.add_child(delay)
	S.space(column, 5)
	metric_note = S.text("", 9, Color("8493aa"))
	column.add_child(metric_note)
	resized.connect(_layout)

func _layout() -> void:
	grid.columns = 2 if size.x < 340 else 3
	for label in values: label.add_theme_font_size_override("font_size", 13 if size.x < 450 else 16)

func render(state: Dictionary) -> void:
	var metrics: Dictionary = state.get("metrics", {})
	var connected: bool = state.get("runState") == "connected"
	var live: bool = connected and metrics.get("available", false) and not metrics.get("stale", false)
	var sampled: bool = metrics.get("available", false) or metrics.get("updatedAt") != null
	var data := [S.bytes(metrics.get("uploadRate", 0)) + "/s" if live else "—", S.bytes(metrics.get("downloadRate", 0)) + "/s" if live else "—", str(int(metrics.get("applicationConnections", 0))) if live else "—", S.bytes(metrics.uploadBytes) if sampled and metrics.has("uploadBytes") else "—", S.bytes(metrics.downloadBytes) if sampled and metrics.has("downloadBytes") else "—", S.bytes(metrics.get("memoryBytes", 0)) if live and metrics.get("memoryBytes", 0) > 0 else "—"]
	for i in range(values.size()): values[i].text = data[i]
	chart.points = metrics.get("history", [])
	chart.queue_redraw()
	var waiting: bool = metrics.get("delayPending", false) or state.get("commandPending", {}).has("delay")
	delay.disabled = not connected or waiting or state.get("selectionPending", false)
	delay.text = "检测中…" if waiting else "检测延迟"
	metric_note.text = "按计费口径显示，结算以服务端为准 · " + ("计费数据暂不可用，保留最近数据" if not metrics.get("available", false) else "计费同步已过期" if metrics.get("stale", false) else "每秒更新 · 最近 60 秒")
	var duration := "—"
	if (connected or state.get("runState") == "recovering") and metrics.get("connectedAt") != null:
		var seconds := maxi(0, int(Time.get_unix_time_from_system() - S.unix_timestamp(str(metrics.connectedAt))))
		duration = "%02d:%02d:%02d" % [seconds / 3600, seconds / 60 % 60, seconds % 60]
	session.text = "◷  连接时长  " + duration
	latency.text = "代理延迟  " + (str(int(metrics.delayMs)) + " ms" if connected and metrics.get("delayMs") != null else "—")
