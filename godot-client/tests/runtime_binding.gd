extends SceneTree
## Use the same executable resolver as editor Play, then speak over real pipes
## with an empty isolated state directory. Never read the user's login or VPN.
const Bridge = preload("res://runtime/bridge.gd")
var failures: Array[String] = []
var replies: Array = []

func _initialize() -> void:
	call_deferred("_run")

func check(ok: bool, message: String) -> void:
	if not ok: failures.append(message); push_error(message)

func _run() -> void:
	var bridge := Bridge.new()
	var executable: String = bridge.runtime_executable().simplify_path()
	check(executable.ends_with("/versions/bundled/hqip-runtime.exe"), "Editor selected a legacy flat runtime")
	var manifest: Dictionary = JSON.parse_string(FileAccess.get_file_as_string("res://../dist/godot-build.json"))
	var isolated := ProjectSettings.globalize_path("res://../.diagnostics/runtime-binding-%d" % OS.get_process_id())
	bridge._process_info = OS.execute_with_pipe(executable, ["--state-dir", isolated], false)
	if bridge._process_info.is_empty():
		push_error("Could not start isolated runtime")
		bridge.free()
		quit(1)
		return
	var pid: int = bridge._process_info.pid
	bridge.replied.connect(func(method, result, error): replies.append([method, result, error]))
	root.add_child(bridge)
	for index in range(5):
		bridge.call_command("state")
		var deadline := Time.get_ticks_msec() + 5000
		while replies.size() <= index and Time.get_ticks_msec() < deadline: await process_frame
		if replies.size() <= index:
			check(false, "Runtime state timed out")
			break
		var reply: Array = replies[index]
		check(reply[2] == "" and reply[1] is Dictionary, "Runtime did not return a valid state")
		if not reply[1] is Dictionary: break
		var state: Dictionary = reply[1]
		check(state.version == manifest.version, "Editor runtime version differs from build manifest")
		check(int(state.get("snapshotSequence", 0)) == index + 1, "Real runtime did not stamp captures monotonically")
		check(not state.loggedIn and not state.connectionIntent and state.runState == "disconnected", "Isolated runtime did not remain idle")
		check(not state.nodeTests.pending and not state.metrics.delayPending, "Idle runtime started latency work")
	bridge.close()
	var deadline := Time.get_ticks_msec() + 5000
	while OS.is_process_running(pid) and Time.get_ticks_msec() < deadline: await process_frame
	check(not OS.is_process_running(pid), "Isolated runtime did not exit on pipe close")
	if OS.is_process_running(pid): OS.kill(pid) # Only this test-owned child.
	bridge.free()
	print("PASS editor runtime binding: ", manifest.version if failures.is_empty() else "FAIL")
	quit(0 if failures.is_empty() else 1)
