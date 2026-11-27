import 'package:material_ui/material_ui.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hqip_preview/ui/traffic.dart';

import '../tool/preview_fixture.dart';
import 'layout_test.dart' show mount, navigate;

void main() {
  testWidgets(
    'dashboard changes route and TUN while preserving the other option',
    (tester) async {
      final transport = PreviewTransport();
      await mount(tester, transport, size: const Size(1280, 900));
      await tester.tap(find.byKey(const ValueKey('quick-mode-global')));
      await tester.pumpAndSettle();
      expect(transport.requests.where((r) => r.method == 'mode').last.params, {
        'mode': 'global',
        'tun': false,
      });
      await tester.tap(find.byKey(const ValueKey('quick-tun')));
      await tester.pumpAndSettle();
      expect(transport.requests.where((r) => r.method == 'mode').last.params, {
        'mode': 'global',
        'tun': true,
      });
      transport.emit({'selectionPending': true});
      await tester.pumpAndSettle();
      expect(
        tester
            .widget<ChoiceChip>(find.byKey(const ValueKey('quick-mode-rule')))
            .onSelected,
        isNull,
      );
      expect(
        tester
            .widget<Switch>(find.byKey(const ValueKey('quick-tun')))
            .onChanged,
        isNull,
      );
    },
  );

  testWidgets('Android route controls retain VPN mode', (tester) async {
    final transport = PreviewTransport();
    await mount(
      tester,
      transport,
      size: const Size(412, 900),
      platform: TargetPlatform.android,
    );
    expect(find.byKey(const ValueKey('quick-tun')), findsNothing);
    await tester.ensureVisible(find.byKey(const ValueKey('quick-mode-global')));
    await tester.tap(find.byKey(const ValueKey('quick-mode-global')));
    await tester.pumpAndSettle();
    expect(transport.requests.where((r) => r.method == 'mode').single.params, {
      'mode': 'global',
      'tun': true,
    });
  });

  testWidgets(
    'subscription actions use package identity and expose bandwidth and dates',
    (tester) async {
      final transport = PreviewTransport();
      final items = transport.data['subscriptions'] as List;
      items[1].addAll({
        'bandwidthMbps': 120,
        'createdAt': '2026-10-01T00:00:00Z',
      });
      await mount(tester, transport, size: const Size(1280, 900));
      await navigate(tester, 1);
      expect(find.text('120 Mbps'), findsOneWidget);
      expect(find.text('2026/10/01'), findsOneWidget);
      expect(
        tester
            .widget<OutlinedButton>(
              find.byKey(const ValueKey('subscription-standard')),
            )
            .onPressed,
        isNull,
      );
      final choose = find.byKey(const ValueKey('subscription-travel'));
      await tester.ensureVisible(choose);
      await tester.tap(choose);
      await tester.pumpAndSettle();
      expect(
        transport.requests
            .where((r) => r.method == 'subscription')
            .single
            .params,
        {'subscriptionId': 'travel'},
      );
      transport.emit({
        'selectionPending': true,
        'desiredSubscriptionId': 'travel',
      });
      await tester.pumpAndSettle();
      expect(find.text('正在切换订阅…'), findsOneWidget);
      expect(tester.widget<OutlinedButton>(choose).onPressed, isNull);
    },
  );

  testWidgets(
    'pending and disabled subscriptions are not presented as expired',
    (tester) async {
      final transport = PreviewTransport();
      final items = transport.data['subscriptions'] as List;
      items[0].addAll({
        'status': 'disabled',
        'expiresAt': '2020-01-01T00:00:00Z',
      });
      items[1]['status'] = 'pending';
      await mount(tester, transport, size: const Size(1280, 900));
      await navigate(tester, 1);
      expect(find.text('已停用'), findsOneWidget);
      expect(find.text('待生效'), findsOneWidget);
      for (final id in ['standard', 'travel']) {
        expect(
          tester
              .widget<OutlinedButton>(find.byKey(ValueKey('subscription-$id')))
              .onPressed,
          isNull,
        );
      }
    },
  );

  testWidgets(
    'node catalog runs explicit batch probes and renders shared outcomes',
    (tester) async {
      final transport = PreviewTransport();
      await mount(tester, transport, size: const Size(1280, 900));
      expect(transport.requests.any((r) => r.method == 'testNodes'), isFalse);
      await tester.tap(find.byKey(const ValueKey('node-selector')));
      await tester.pumpAndSettle();
      await tester.tap(find.byKey(const ValueKey('test-all-nodes')));
      await tester.pumpAndSettle();
      expect(
        transport.requests.where((r) => r.method == 'testNodes').length,
        1,
      );
      transport.emit({
        'nodeTests': {
          'pending': true,
          'subscriptionId': '',
          'results': [
            {'nodeId': 'tokyo', 'status': 'success', 'delayMs': 42},
            {'nodeId': 'singapore', 'status': 'timeout', 'delayMs': 0},
          ],
        },
      });
      await tester.pumpAndSettle();
      expect(find.text('42 ms'), findsNWidgets(2));
      expect(find.text('连接超时'), findsOneWidget);
      await tester.tap(find.byKey(const ValueKey('test-all-nodes')));
      await tester.pumpAndSettle();
      expect(
        transport.requests.where((r) => r.method == 'cancelTest').length,
        1,
      );
      expect(
        transport.requests.any(
          (r) => r.method == 'node' || r.method == 'connect',
        ),
        isFalse,
      );
    },
  );

  testWidgets(
    'single-package probe never leaks its result to another subscription',
    (tester) async {
      final transport = PreviewTransport(
        overrides: {
          'nodeTests': {
            'pending': false,
            'subscriptionId': 'travel',
            'nodeId': 'tokyo',
            'results': [
              {'nodeId': 'tokyo', 'status': 'certificate_error', 'delayMs': 0},
            ],
          },
        },
      );
      await mount(tester, transport, size: const Size(1280, 900));
      await tester.tap(find.byKey(const ValueKey('node-selector')));
      await tester.pumpAndSettle();
      expect(find.text('证书验证失败'), findsOneWidget);
      expect(find.text('尚未检测延迟'), findsNWidgets(2));
    },
  );

  testWidgets(
    'traffic exposes cumulative counters and hides stale live metrics',
    (tester) async {
      final transport = PreviewTransport(
        overrides: {
          'runState': 'connected',
          'metrics': {
            'available': true,
            'stale': false,
            'applicationConnections': 17,
            'memoryBytes': 64000000,
            'uploadBytes': 2000000,
            'downloadBytes': 9000000,
            'connectedAt': DateTime.now()
                .subtract(const Duration(hours: 1))
                .toIso8601String(),
          },
        },
      );
      await mount(tester, transport, size: const Size(1280, 1200));
      expect(find.text('累计上传'), findsOneWidget);
      expect(find.text('累计下载'), findsOneWidget);
      expect(find.text('17'), findsOneWidget);
      expect(find.text('64 MB'), findsOneWidget);
      expect(find.textContaining(RegExp(r'^01:00:\d{2}$')), findsOneWidget);
      transport.emit({
        'metrics': {
          ...transport.data['metrics'] as Map<String, dynamic>,
          'stale': true,
        },
      });
      await tester.pumpAndSettle();
      expect(find.text('数据已暂停'), findsOneWidget);
      expect(find.text('17'), findsNothing);
      expect(find.text('64 MB'), findsNothing);
      expect(find.text('2.0 MB'), findsOneWidget);
    },
  );

  testWidgets('settings show runtime revisions, updated sync state and help', (
    tester,
  ) async {
    final transport = PreviewTransport(
      overrides: {
        'coreVersion': '1.14.4',
        'appliedConfigRevision': 8,
        'rulesVersion': 6,
        'lastSync': '2026-11-26T01:02:03Z',
      },
    );
    await mount(tester, transport, size: const Size(1280, 900));
    await navigate(tester, 3);
    expect(find.text('sing-box 1.14.4'), findsOneWidget);
    expect(find.text('v8 / v6'), findsOneWidget);
    transport.emit({'controlUnavailable': true, 'rulesVersion': 7});
    await tester.pumpAndSettle();
    expect(find.text('账户暂未同步'), findsOneWidget);
    expect(find.text('v8 / v7'), findsOneWidget);
    final help = find.byKey(const ValueKey('usage-help'));
    await tester.ensureVisible(help);
    await tester.tap(help);
    await tester.pumpAndSettle();
    expect(find.text('两种延迟检测'), findsOneWidget);
    await tester.tap(find.text('知道了'));
    await tester.pumpAndSettle();
    expect(find.byType(AlertDialog), findsNothing);
  });

  test(
    'connection duration clamps future timestamps and clears when disconnected',
    () {
      final now = DateTime.utc(2026, 11, 26, 2, 3, 4);
      expect(
        connectionDuration('2026-11-26T00:00:00Z', true, now: now),
        '02:03:04',
      );
      expect(
        connectionDuration('2027-01-01T00:00:00Z', true, now: now),
        '00:00:00',
      );
      expect(connectionDuration('bad', true, now: now), '—');
      expect(connectionDuration('2026-11-26T00:00:00Z', false, now: now), '—');
    },
  );
}
