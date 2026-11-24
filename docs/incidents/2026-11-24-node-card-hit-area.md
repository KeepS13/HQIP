# 节点卡片右下角点击死区

日期：2026-11-24。修复版本：`5.0.0-preview.15`（`5000015`）。归属 Godot 节点选择展示层。

用户标记的右下角区域对应 `node_picker.gd::_add_choice` 中的透明 `Control` 弹性占位控件。它用于把选中勾号推到右侧，却保留默认 `MOUSE_FILTER_STOP`，先于父级 Button 命中并截断点击。外层 margin/column 已忽略鼠标，并不能让其后代自动忽略鼠标；因此标题、边缘、勾号可点击，而下方空白存在死区。

修复只将卡片底行及其空白占位控件设为 `MOUSE_FILTER_IGNORE`。整张卡仍由同一个 Button 处理选择、禁用和焦点，原有每次选择单个 node 命令保持不变；不增加覆盖按钮、事件转发、网络请求或每帧检查。

使用 `node_card_hit_area.gd` 向真实 Godot Viewport 注入鼠标移动、按下及松开事件，不直接触发 pressed 信号。修改前，1040/640/360 宽度下共 9 个右下角点失败；修改后，两个卡片（已选和未选）各 7 个位置、3 种宽度共 42 点通过，禁用卡片保持不可选择。instant_selection、interaction_regression、ui_smoke 也通过。此前直接发送 pressed 信号的选择测试绕过了鼠标命中，因此没有发现这一死区。

GitNexus 对 `_add_choice` 的 impact 返回 UNKNOWN（GDScript 函数未索引）。已用文本确认唯一调用为本文件 render，结合真实输入路由回归验证变更范围；没有将 UNKNOWN 当作低风险。未修改原生连接、测速及同步逻辑，也未操作用户当前运行窗口。
