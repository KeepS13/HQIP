# 未连接测速与节点／连接状态回跳

日期：2026-11-24。能力：`client-runtime` / Godot 展示桥接。修复版本：`5.0.0-preview.14`，序号 `5000014`。

## 现场与原因

1. 当前 HQIP Godot 编辑器场景实际启动 `dist/godot-windows/hqip-runtime.exe`（11 月 23 日残留文件）。新构建一直输出到 `versions/bundled`，编辑器路径没有同步更新。进程路径、独立空会话版本查询和运行中设置页均确认旧实例为 `4.8.1-dev`；排查前 bundled 版本为 `5.0.0-preview.13`。旧二进制仍有 `Controller.Run → startAutomaticNodeTests → startNodeTestsLocked` 调用链，说明仅检查新源码及预览包没有覆盖用户实际入口。
2. 桥接层只防止旧轮询覆盖选择回执，没有防止迟到的选择完整快照覆盖更新的断开状态。确定性复现：选择回执先采集 connected/DelayPending，断开后的 state 先到达，随后旧选择回执到达；界面重新显示已连接／延迟检测中。该路径属于过时显示，不证明原生重新连接。
3. Disconnect 在等待 routeMu 前已撤销连接意图，但原生延迟入口只检查 RunState。此时旧状态还可能为 connected；明确构造该窗口后，手动延迟请求仍进入内核，计数从 1 增至 2。它是断开边界漏洞，并非当前源码存在空闲定时测速循环。

排查没有把断开状态下的历史测速值直接当作新网络请求；旧运行时证据、界面回写复现、原生请求计数分别记录。

## 改动与性能

- 编辑器只解析当前 bundled 运行时；缺失时报错，不自动加载旧平铺文件。显式开发覆盖仍保留。旧布尔选择回执明确报不匹配，不默默接受。
- Go Bridge 给 state/node/subscription 的完整 View 添加 `snapshotSequence`。锁覆盖采集与编号，命令和网络不持有此锁；Windows 管道及 Android JNI 共用此实现。GDScript 按采集序号接收快照，旧命令回执结束等待但不回写状态，保留连续选择队列。
- 延迟任务必须仍有连接意图、已连接状态及实际订阅/节点 ID；断开撤销意图即拒绝新检测。连接成功后的单次自动检测、手动列表测速保持原契约。
- 没有新增账户查询、补发 state、节点探测、周期任务或重试。每秒单在途 UI 状态读取保持原频率；编号只增加少量本地 JSON 字节和短内存锁。

## 复现及验证

修改前 `snapshot_order.gd` 断言两项失败（旧连接复活、空闲显示检测中）；`TestDelayRejectedWhileDisconnectWaitsForRoute` 失败（断开后内核检测数增至 2）。修改后均通过。

- `go test -race ./internal/client ./internal/clientbridge ./internal/winclient -count=1 -timeout 150s` 通过，包含当前连接自动检测一次、切换取消旧检测、手动断开取消排队切换、空闲选择零探测，以及新的快照采集/断开并发测试。
- 8 项 Godot 回归通过：snapshot_order、snapshot_compatibility、connected_delay、active_selection、instant_selection、interaction_regression、subscription_probe_regression、ui_smoke。即时选择夹具约 4.5 ms，不是硬件性能保证。
- `runtime_binding.gd` 从实际编辑器解析路径启动真实原生管道，使用独立空会话，5 次读取均保持断开、无测速，版本 `5.0.0-preview.14` 且编号递增，关闭管道后子进程退出。

GitNexus 全量重建后命名 Go 符号恢复正确。`prepareDelayLocked` upstream 风险 HIGH（直接调用 applyLatest、MeasureDelay、recoverConnection），修改前已说明。Bridge/dispatch 为 LOW。GDScript 函数未被索引，impact 为 UNKNOWN，使用调用点和实际桥接测试补充；全库流程分析存在裁剪，不能宣称全库依赖检查完全无遗漏。没有提交或线上部署。

真实 VPN 接管、跨节点出站和移动端实机性能不由上述隔离测试证明。
