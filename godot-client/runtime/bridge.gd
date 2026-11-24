extends Node
## Private native command channel. Neither requests nor replies carry tokens.
signal replied(method: String, result: Variant, error: String)

var _process_info: Dictionary = {}
var _plugin: Object
var _next_id := 0
var _pending: Dictionary = {}
var _buffer := PackedByteArray()
var _closed := false
var _state_barrier := 0
var _snapshot_sequence := 0

func runtime_executable() -> String:
	var executable := OS.get_executable_path().get_base_dir().path_join("hqip-runtime.exe")
	if OS.has_feature("editor"):
		# The build writes the entire native payload here. The former flat path
		# can still contain a runnable 4.x binary with automatic catalog probes.
		executable = ProjectSettings.globalize_path("res://../dist/godot-windows/versions/bundled/hqip-runtime.exe")
	for argument in OS.get_cmdline_user_args():
		if argument.begins_with("--runtime="):
			executable = argument.trim_prefix("--runtime=")
	return executable

func start() -> String:
	if OS.get_name() == "Android":
		if not Engine.has_singleton("HQIPRuntime"):
			return "安装包缺少原生 VPN 模块"
		_plugin = Engine.get_singleton("HQIPRuntime")
		return _plugin.initialize()
	var executable := runtime_executable()
	if not FileAccess.file_exists(executable):
		return "缺少当前客户端运行时，请先构建或重新安装完整客户端"
	_process_info = OS.execute_with_pipe(executable, [], false)
	if _process_info.is_empty():
		return "无法启动客户端运行时，请使用完整安装包"
	return ""

func call_command(method: String, params: Dictionary = {}) -> bool:
	if _closed or (_plugin == null and _process_info.is_empty()):
		return false
	# Only one state query may be pending. Long commands retain separate IDs.
	if method == "state" and _pending.values().has("state"):
		return true
	_next_id += 1
	if method in ["node", "subscription"]: _state_barrier = _next_id
	_pending[_next_id] = method
	var raw := JSON.stringify({"id": _next_id, "method": method, "params": params})
	if _plugin != null:
		_plugin.submit(raw)
	else:
		var pipe: FileAccess = _process_info.stdio
		pipe.store_string(raw + "\n")
		pipe.flush()
	return true

func _process(_delta: float) -> void:
	if _closed:
		return
	if _plugin != null:
		for _i in range(32):
			var raw: String = _plugin.poll()
			if raw.is_empty(): break
			_receive(raw)
	elif not _process_info.is_empty():
		var pipe: FileAccess = _process_info.stdio
		var chunk := pipe.get_buffer(65536)
		if not chunk.is_empty(): _buffer.append_array(chunk)
		if _buffer.size() > 2097152:
			close()
			replied.emit("runtime", null, "客户端运行时响应过大")
			return
		var newline := _buffer.find(10)
		while newline >= 0:
			_receive(_buffer.slice(0, newline).get_string_from_utf8())
			_buffer = _buffer.slice(newline + 1)
			newline = _buffer.find(10)
		# Drain stderr without persisting potentially sensitive runtime details.
		var error_pipe: FileAccess = _process_info.stderr
		error_pipe.get_buffer(4096)
		if not OS.is_process_running(_process_info.pid):
			close()
			replied.emit("runtime", null, "客户端运行时已停止，请重新打开应用")

func _receive(raw: String) -> void:
	var response = JSON.parse_string(raw)
	if not response is Dictionary: return
	var id := int(response.get("id", 0))
	if not _pending.has(id): return
	var method: String = _pending[id]
	_pending.erase(id)
	var result: Variant = response.get("result")
	var error := str(response.get("error", ""))
	if error.is_empty() and method in ["node", "subscription"] and not result is Dictionary:
		replied.emit(method, null, "客户端运行时不匹配，请重新构建或安装完整客户端")
		return
	# Native capture order is independent of request IDs and delivery order.
	# Retire an old command acknowledgement, but never apply its old connection,
	# latency or account fields over a newer snapshot (including Disconnect).
	var sequence := int(result.get("snapshotSequence", 0)) if result is Dictionary else 0
	if sequence > 0:
		if sequence <= _snapshot_sequence:
			if method != "state": replied.emit(method, null, error)
			return
		_snapshot_sequence = sequence
	# The selection acknowledgement carries a full confirmed View. Drop older
	# unstamped reads from older runtimes without launching redundant polls.
	if method in ["node", "subscription"]: _state_barrier = _next_id + 1
	if sequence == 0 and method == "state" and id < _state_barrier:
		return
	replied.emit(method, result, error)

func close() -> void:
	_closed = true
	if not _process_info.is_empty():
		_process_info.stdio.close()
		_process_info.stderr.close()
		_process_info.clear()
	_pending.clear()

func _exit_tree() -> void:
	close()
