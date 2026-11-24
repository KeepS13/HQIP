extends Control
## Paint-only view of native metrics; never samples the network itself.
const S = preload("res://ui/style.gd")
var points: Array = []
var mini := false

func _ready() -> void:
	custom_minimum_size.y = 53
	resized.connect(queue_redraw)

func _draw() -> void:
	for fraction in ([0.5, 0.92] if mini else [10.0 / 105, 52.0 / 105, 95.0 / 105]):
		if mini: draw_dashed_line(Vector2(0, size.y * fraction), Vector2(size.x, size.y * fraction), Color("273141"), 1, 3)
		else: draw_line(Vector2(0, size.y * fraction), Vector2(size.x, size.y * fraction), Color("283341"))
	if points.is_empty(): return
	var peak := 1024.0
	for point in points: peak = maxf(peak, maxf(float(point.get("uploadRate", 0)), float(point.get("downloadRate", 0))))
	for field in ["uploadRate", "downloadRate"]:
		var line := PackedVector2Array()
		for i in range(points.size()):
			var x := size.x if points.size() == 1 else float(i) / (points.size() - 1) * size.x
			var y := size.y * ((0.92 if mini else 95.0 / 105) - (0.8 if mini else 85.0 / 105) * maxf(0, float(points[i].get(field, 0))) / peak)
			line.append(Vector2(2 + x * 0.975 if mini else x, y))
		var color := Color("b8a7ff") if field == "uploadRate" else Color("79d5bf")
		if line.size() > 1: draw_polyline(line, color, 1.5 if mini else 2, true)
		else: draw_circle(line[0], 2, color)
