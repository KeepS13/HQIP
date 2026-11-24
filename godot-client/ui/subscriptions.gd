extends VBoxContainer
## Informational account inventory. Cards own no selection/connection state;
## polling updates values in place, while resizing only changes grid columns.
signal command(method: String, params: Dictionary)
const S = preload("res://ui/style.gd")
const Card = preload("res://ui/subscription_card.gd")
var list: GridContainer
var compact := false
var _key := ""
var _rows: Dictionary = {}
var empty: Label
var shop: Button

func _ready() -> void:
	add_theme_constant_override("separation", 20)
	if not compact:
		var note := S.text("每份订阅拥有独立的流量额度和有效期，可在这里查看使用情况。", 12, S.MUTED)
		add_child(note)
	list = GridContainer.new()
	list.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	list.add_theme_constant_override("h_separation", 18)
	list.add_theme_constant_override("v_separation", 18)
	add_child(list)
	empty = S.text("还没有订阅，前往商城挑选适合你的套餐。", 12, S.MUTED)
	add_child(empty)
	if not compact: _build_shop()
	resized.connect(_layout)

func _build_shop() -> void:
	var body := S.card(self)
	body.get_parent().add_theme_stylebox_override("panel", S.panel(Color("1c2035"), 14, 22, Color("393651")))
	var heading := S.text("为下一段旅程，准备更多流量", 17)
	S.typography(heading, 600)
	body.add_child(heading)
	S.space(body, 9)
	body.add_child(S.text("探索更多订阅套餐，按需选择流量与带宽。前往网页商城查看详情并购买。", 12, S.MUTED))
	S.space(body, 18)
	shop = S.button("浏览订阅商城  ↗", func(): command.emit("shop", {}))
	shop.custom_minimum_size.y = 44
	shop.size_flags_horizontal = Control.SIZE_SHRINK_BEGIN
	S.button_surface(shop, S.panel(Color("35304f"), 8, 12, Color("736492")))
	body.add_child(shop)

func render(state: Dictionary) -> void:
	var items: Array = state.get("subscriptions", [])
	var ids := []
	for item in items: ids.append(item.id)
	var key := JSON.stringify(ids)
	if key != _key:
		_key = key
		_rows.clear()
		S.clear_children(list)
		for item in items:
			var card := Card.new()
			list.add_child(card)
			_rows[item.id] = card
	for item in items: _rows[item.id].render(item)
	list.visible = not items.is_empty()
	empty.visible = items.is_empty()
	_layout()

func _layout() -> void:
	if list == null: return
	# Use the content width, not the window width: the dashboard has an inset
	# and the sidebar may be open. A sole card always occupies the full row.
	list.columns = mini(maxi(1, list.get_child_count()), clampi(int((size.x + 18) / 303), 1, 3))
