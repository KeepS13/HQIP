import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hqip_preview/runtime/models.dart';
import 'package:hqip_preview/runtime/session.dart';

import 'session_test.dart' show FakeTransport;

class LoginTransport extends FakeTransport {
  bool saveBeforeFailure = false;
  final loginServers = <String?>[];
  Completer<Json>? heldSubscription;
  @override
  Future<Json> call(String method, [Json params = const {}]) async {
    if (method == 'login') {
      requests.add(method);
      loginServers.add(params['server'] as String?);
      loggedIn = saveBeforeFailure;
      return {'error': '账户服务连接超时，将自动重试；登录状态已保留'};
    }
    if (method == 'subscription' && heldSubscription != null) {
      requests.add(method);
      return heldSubscription!.future;
    }
    return super.call(method, params);
  }
}

void main() {
  test(
    'failed login retries original server and does not claim saved credentials',
    () async {
      final transport = LoginTransport()..loggedIn = false;
      final container = ProviderContainer(
        overrides: [transportProvider.overrideWithValue(transport)],
      );
      addTearDown(container.dispose);
      final session = container.read(sessionProvider.notifier);
      await Future<void>.delayed(Duration.zero);
      await session.command('login', {'server': 'https://custom.example'});
      expect(container.read(sessionProvider).error, isNot(contains('登录状态已保留')));
      expect(container.read(sessionProvider).error, isNot(contains('自动重试')));
      await session.retryAccount();
      expect(transport.loginServers, [
        'https://custom.example',
        'https://custom.example',
      ]);
      expect(transport.requests, isNot(contains('refresh')));
      expect(transport.requests, isNot(contains('connect')));
    },
  );

  test(
    'saved login with failed sync refreshes state while polling is suspended',
    () async {
      final transport = LoginTransport()
        ..loggedIn = false
        ..saveBeforeFailure = true;
      final container = ProviderContainer(
        overrides: [transportProvider.overrideWithValue(transport)],
      );
      addTearDown(container.dispose);
      final session = container.read(sessionProvider.notifier);
      await Future<void>.delayed(Duration.zero);
      await session.visibility(false);
      await session.command('login');
      expect(container.read(sessionProvider).snapshot.loggedIn, isTrue);
      expect(container.read(sessionProvider).error, contains('登录状态已保留'));
      await session.retryAccount();
      expect(transport.requests, contains('refresh'));
      expect(transport.loginServers, hasLength(1));
      expect(transport.requests, isNot(contains('connect')));
    },
  );

  test(
    'subscription transition blocks overlapping node and connect requests',
    () async {
      final transport = LoginTransport()..heldSubscription = Completer<Json>();
      final container = ProviderContainer(
        overrides: [transportProvider.overrideWithValue(transport)],
      );
      addTearDown(container.dispose);
      final session = container.read(sessionProvider.notifier);
      await Future<void>.delayed(Duration.zero);
      final selecting = session.command('subscription', {
        'subscriptionId': 's',
      });
      await session.select(const NodeChoice('b', 's', 'B', true));
      await session.command('connect');
      expect(transport.requests, isNot(contains('node:b')));
      expect(transport.requests, isNot(contains('connect')));
      transport.heldSubscription!.complete({'result': transport.snapshot()});
      await selecting;
    },
  );
}
