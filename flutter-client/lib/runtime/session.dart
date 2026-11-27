import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'models.dart';
import 'transport.dart';

final transportProvider = Provider<RuntimeTransport>(
  (ref) => platformTransport(),
);
final sessionProvider = NotifierProvider<Session, AppState>(Session.new);

class AppState {
  AppState({
    Snapshot? snapshot,
    this.ready = false,
    this.error = '',
    this.busy = const {},
    this.draft,
  }) : snapshot = snapshot ?? Snapshot({});
  final Snapshot snapshot;
  final bool ready;
  final String error;
  final Set<String> busy;
  final NodeChoice? draft;
  NodeChoice? get selected =>
      draft ??
      snapshot.choice(snapshot.selectedId, snapshot.selectedSubscription);
  AppState copy({
    Snapshot? snapshot,
    bool? ready,
    String? error,
    Set<String>? busy,
    NodeChoice? draft,
    bool clearDraft = false,
  }) => AppState(
    snapshot: snapshot ?? this.snapshot,
    ready: ready ?? this.ready,
    error: error ?? this.error,
    busy: busy ?? this.busy,
    draft: clearDraft ? null : draft ?? this.draft,
  );
}

class Session extends Notifier<AppState> {
  late RuntimeTransport _transport;
  bool _disposed = false;
  bool _selecting = false;
  int _selectionRevision = 0;
  NodeChoice? _queued;
  Json _loginParams = const {};
  @override
  AppState build() {
    _transport = ref.watch(transportProvider);
    ref.onDispose(() {
      _disposed = true;
      unawaited(_transport.close());
    });
    scheduleMicrotask(_start);
    return AppState();
  }

  Future<void> _start() async {
    _transport.events.listen(
      _accept,
      onError: (Object e) {
        if (!_disposed) state = state.copy(error: _message(e));
      },
    );
    try {
      await _transport.start();
      final result = await _transport.call('state');
      _accept(object(result['result']));
      if (result['error'] != null) throw StateError(result['error'] as String);
      if (!_disposed) {
        state = state.copy(ready: true);
        await _transport.watch(true);
        await _transport.call('uiReady');
      }
    } catch (e) {
      if (!_disposed) state = state.copy(error: _message(e));
    }
  }

  void _accept(Json raw) {
    if (_disposed) return;
    final next = Snapshot({...state.snapshot.raw, ...raw});
    if (next.sequence <= state.snapshot.sequence &&
        state.snapshot.sequence > 0) {
      return;
    }
    state = state.copy(snapshot: next, clearDraft: !next.loggedIn);
    if (!next.loggedIn) _queued = null;
  }

  Future<void> command(String method, [Json params = const {}]) async {
    if (state.busy.contains(method)) return;
    if (['connect', 'subscription'].contains(method) &&
        (state.draft != null ||
            _selecting ||
            state.busy.contains('subscription'))) {
      return;
    }
    if (method == 'login') _loginParams = Map.of(params);
    state = state.copy(busy: {...state.busy, method}, error: '');
    try {
      final response = await _transport.call(method, params);
      if (response['error'] is String &&
          (response['error'] as String).isNotEmpty) {
        throw StateError(response['error'] as String);
      }
      if (response['result'] is Map &&
          object(response['result']).containsKey('snapshotSequence')) {
        _accept(object(response['result']));
      }
      if (!_disposed && !['shop', 'account', 'uiReady'].contains(method)) {
        final response = await _transport.call('state');
        _accept(object(response['result']));
      }
    } catch (e) {
      // Login can save credentials before an initial profile sync fails. Fetch
      // that state even while the browser has suspended normal UI polling.
      if (method == 'login' && !_disposed) {
        try {
          final response = await _transport.call('state');
          _accept(object(response['result']));
        } catch (_) {}
      }
      if (!_disposed) {
        var message = _message(e);
        if (method == 'login' && !state.snapshot.loggedIn) {
          message = message
              .replaceAll('将自动重试', '请检查网络后重试')
              .replaceAll('；登录状态已保留', '');
        }
        state = state.copy(error: message);
      }
    } finally {
      if (!_disposed) state = state.copy(busy: {...state.busy}..remove(method));
    }
  }

  Future<void> retryAccount() => state.snapshot.loggedIn
      ? command('refresh')
      : command('login', _loginParams);

  Future<void> select(NodeChoice choice) async {
    if (!choice.ready ||
        state.busy.contains('subscription') ||
        !state.snapshot.loggedIn ||
        !(state.snapshot.package(choice.subscriptionId)?.valid ?? false)) {
      return;
    }
    if (state.selected?.key == choice.key && !_selecting) return;
    _selectionRevision++;
    _queued = choice;
    state = state.copy(draft: choice, error: '');
    if (_selecting) return;
    _selecting = true;
    try {
      while (_queued != null && !_disposed) {
        final desired = _queued!;
        _queued = null;
        final revision = _selectionRevision;
        try {
          final response = await _transport.call('node', {
            'nodeId': desired.id,
            'subscriptionId': desired.subscriptionId,
          });
          if (response['error'] is String) {
            throw StateError(response['error'] as String);
          }
          _accept(object(response['result']));
          if (revision == _selectionRevision && !_disposed) {
            state = state.copy(clearDraft: true);
          }
        } catch (e) {
          if (revision == _selectionRevision && !_disposed) {
            state = state.copy(clearDraft: true, error: _message(e));
          }
        }
      }
    } finally {
      _selecting = false;
    }
  }

  Future<void> visibility(bool visible) async {
    if (!state.ready || _disposed) return;
    try {
      await _transport.watch(visible);
      if (visible) {
        final r = await _transport.call('state');
        _accept(object(r['result']));
      }
    } catch (_) {}
  }

  String _message(Object error) =>
      error is StateError ? error.message.toString() : '操作暂未完成，请重试';
}
