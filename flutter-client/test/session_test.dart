import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hqip_preview/runtime/models.dart';
import 'package:hqip_preview/runtime/session.dart';
import 'package:hqip_preview/runtime/transport.dart';

class FakeTransport implements RuntimeTransport {
  final controller = StreamController<Json>.broadcast();
  final requests = <String>[];
  final selections = <Completer<Json>>[];
  bool heldSelections = false;
  bool loggedIn = true;
  int sequence = 1;
  Json snapshot() => {
    'snapshotSequence': sequence++,
    'loggedIn': loggedIn,
    'runState': 'disconnected',
    'desiredSubscriptionId': 's',
    'desiredNodeId': 'a',
    'version': '0.1.0-preview.1',
    'subscriptions': [
      {
        'id': 's',
        'templateName': '测试订阅',
        'status': 'active',
        'expiresAt': '2099-01-01T00:00:00Z',
        'quotaBytes': 100000000000,
        'usedBytes': 18000000000,
      },
    ],
    'nodeChoices': [
      for (final id in ['a', 'b', 'c'])
        {'nodeId': id, 'subscriptionId': 's', 'name': '节点 $id', 'ready': true},
    ],
  };
  @override
  Stream<Json> get events => controller.stream;
  @override
  Future<void> start() async {}
  @override
  Future<void> close() async {
    await controller.close();
  }

  @override
  Future<void> watch(bool enabled) async {
    requests.add(enabled ? 'watch' : 'unwatch');
  }

  @override
  Future<Json> call(String method, [Json params = const {}]) async {
    requests.add(method == 'node' ? 'node:${params['nodeId']}' : method);
    if (method == 'node' && heldSelections) {
      final result = Completer<Json>();
      selections.add(result);
      return result.future;
    }
    return {'result': snapshot()};
  }
}

void main() {
  test(
    'latest selection survives older replies and queued choices coalesce',
    () async {
      final transport = FakeTransport()..heldSelections = true;
      final container = ProviderContainer(
        overrides: [transportProvider.overrideWithValue(transport)],
      );
      addTearDown(container.dispose);
      final session = container.read(sessionProvider.notifier);
      await Future<void>.delayed(Duration.zero);
      final first = session.select(const NodeChoice('b', 's', 'B', true));
      await session.select(const NodeChoice('a', 's', 'A', true));
      await session.select(const NodeChoice('c', 's', 'C', true));
      transport.controller.add({
        ...transport.snapshot(),
        'snapshotSequence': 20,
        'desiredNodeId': 'a',
      });
      await Future<void>.delayed(Duration.zero);
      transport.selections[0].complete({
        'result': {
          ...transport.snapshot(),
          'snapshotSequence': 10,
          'desiredNodeId': 'b',
        },
      });
      await Future<void>.delayed(Duration.zero);
      expect(container.read(sessionProvider).selected?.id, 'c');
      expect(transport.requests.where((r) => r.startsWith('node:')), [
        'node:b',
        'node:c',
      ]);
      transport.selections[1].complete({
        'result': {
          ...transport.snapshot(),
          'snapshotSequence': 21,
          'desiredNodeId': 'c',
        },
      });
      await first;
      expect(container.read(sessionProvider).selected?.id, 'c');
      expect(container.read(sessionProvider).draft, isNull);
    },
  );

  test(
    'hidden UI stops state subscription and logout drops queued intent',
    () async {
      final transport = FakeTransport()..heldSelections = true;
      final container = ProviderContainer(
        overrides: [transportProvider.overrideWithValue(transport)],
      );
      addTearDown(container.dispose);
      final session = container.read(sessionProvider.notifier);
      await Future<void>.delayed(Duration.zero);
      await session.visibility(false);
      expect(transport.requests.last, 'unwatch');
      final first = session.select(const NodeChoice('b', 's', 'B', true));
      await session.select(const NodeChoice('c', 's', 'C', true));
      transport.controller.add({'snapshotSequence': 50, 'loggedIn': false});
      await Future<void>.delayed(Duration.zero);
      transport.selections[0].complete({
        'result': {...transport.snapshot(), 'snapshotSequence': 49},
      });
      await first;
      expect(container.read(sessionProvider).snapshot.loggedIn, isFalse);
      expect(container.read(sessionProvider).draft, isNull);
      expect(transport.selections.length, 1);
    },
  );
}
