import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter/services.dart';

import 'models.dart';

abstract interface class RuntimeTransport {
  Stream<Json> get events;
  Future<void> start();
  Future<Json> call(String method, [Json params = const {}]);
  Future<void> watch(bool enabled);
  Future<void> close();
}

RuntimeTransport platformTransport() =>
    Platform.isAndroid ? AndroidTransport() : WindowsTransport();

class WindowsTransport implements RuntimeTransport {
  final _events = StreamController<Json>.broadcast();
  final _pending = <int, Completer<Json>>{};
  Process? _process;
  int _nextId = 0;
  bool _closed = false;
  @override
  Stream<Json> get events => _events.stream;
  @override
  Future<void> start() async {
    final override = Platform.environment['HQIP_PREVIEW_RUNTIME'];
    final path =
        override ??
        '${File(Platform.resolvedExecutable).parent.path}\\hqip-runtime.exe';
    _process = await Process.start(path, ['--attach']);
    _process!.stderr.drain<void>();
    _process!.stdout
        .transform(utf8.decoder)
        .transform(const LineSplitter())
        .listen(
          (line) {
            if (line.length > 2097152) {
              _fail(StateError('后台响应过大'));
              return;
            }
            try {
              final message = object(jsonDecode(line));
              if (message['event'] == 'state') {
                _events.add(object(message['result']));
                return;
              }
              final completer = _pending.remove(message['id']);
              if (completer != null && !completer.isCompleted) {
                completer.complete(message);
              }
            } catch (_) {
              _fail(StateError('后台响应格式无效'));
            }
          },
          onError: _fail,
          onDone: () => _fail(StateError('后台连接已关闭，请重新打开应用')),
        );
    unawaited(_process!.exitCode.then((_) => _fail(StateError('后台连接已结束'))));
  }

  void _fail(Object error) {
    for (final pending in _pending.values) {
      if (!pending.isCompleted) pending.completeError(error);
    }
    _pending.clear();
    if (!_closed) _events.addError(error);
  }

  @override
  Future<Json> call(String method, [Json params = const {}]) async {
    final process = _process;
    if (_closed || process == null) throw StateError('后台未连接');
    final id = ++_nextId;
    final completer = Completer<Json>();
    _pending[id] = completer;
    process.stdin.writeln(
      jsonEncode({'id': id, 'method': method, 'params': params}),
    );
    try {
      return await completer.future.timeout(
        Duration(seconds: method == 'login' ? 330 : 90),
      );
    } finally {
      _pending.remove(id);
    }
  }

  @override
  Future<void> watch(bool enabled) async {
    await call(enabled ? 'watchState' : 'unwatchState');
  }

  @override
  Future<void> close() async {
    _closed = true;
    _fail(StateError('界面已关闭'));
    await _process?.stdin.close();
    _process?.kill();
    await _events.close();
  }
}

class AndroidTransport implements RuntimeTransport {
  static const _commands = MethodChannel('hqip/preview/runtime');
  static const _channel = EventChannel('hqip/preview/state');
  final _events = StreamController<Json>.broadcast();
  StreamSubscription<dynamic>? _subscription;
  int _id = 0;
  @override
  Stream<Json> get events => _events.stream;
  @override
  Future<void> start() async {}
  @override
  Future<Json> call(String method, [Json params = const {}]) async {
    final raw = await _commands.invokeMethod<String>(
      'call',
      jsonEncode({'id': ++_id, 'method': method, 'params': params}),
    );
    return object(jsonDecode(raw ?? '{}'));
  }

  @override
  Future<void> watch(bool enabled) async {
    await _subscription?.cancel();
    _subscription = null;
    if (enabled) {
      _subscription = _channel.receiveBroadcastStream().listen(
        (raw) => _events.add(object(jsonDecode(raw as String))),
        onError: _events.addError,
      );
    }
  }

  @override
  Future<void> close() async {
    await watch(false);
    await _events.close();
  }
}
