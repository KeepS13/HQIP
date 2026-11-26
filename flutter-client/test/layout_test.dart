import 'dart:async';

import 'package:flutter/foundation.dart';

import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hqip_preview/app.dart';
import 'package:hqip_preview/runtime/session.dart';
import 'package:hqip_preview/ui/design.dart';
import 'package:hqip_preview/ui/presentation.dart';

import '../tool/preview_fixture.dart';

Future<void> mount(
  WidgetTester tester,
  PreviewTransport transport, {
  Size size = const Size(360, 640),
  double scale = 1,
  TargetPlatform platform = TargetPlatform.windows,
}) async {
  debugDefaultTargetPlatformOverride = platform;
  addTearDown(() => debugDefaultTargetPlatformOverride = null);
  tester.view.physicalSize = size;
  tester.view.devicePixelRatio = 1;
  tester.view.padding = platform == TargetPlatform.android
      ? const FakeViewPadding(top: 24, bottom: 24)
      : const FakeViewPadding();
  addTearDown(tester.view.resetPadding);
  tester.platformDispatcher.textScaleFactorTestValue = scale;
  addTearDown(tester.view.resetPhysicalSize);
  addTearDown(tester.view.resetDevicePixelRatio);
  addTearDown(tester.platformDispatcher.clearTextScaleFactorTestValue);
  await tester.pumpWidget(
    ProviderScope(
      overrides: [transportProvider.overrideWithValue(transport)],
      child: const HqipApp(),
    ),
  );
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 250));
}

Future<void> navigate(WidgetTester tester, int page) async {
  await tester.tap(find.byKey(ValueKey('navigation-$page')));
  await tester.pump();
  await tester.pump(const Duration(milliseconds: 250));
}

void main() {
  for (final size in const [
    Size(320, 640),
    Size(360, 640),
    Size(412, 800),
    Size(900, 520),
    Size(1280, 720),
    Size(1440, 900),
  ]) {
    for (final scale in [1.0, 1.5, 2.0]) {
      for (final logged in [false, true]) {
        testWidgets(
          'all pages ${size.width}x${size.height} scale=$scale logged=$logged',
          (tester) async {
            final transport = PreviewTransport(loggedIn: logged);
            await mount(
              tester,
              transport,
              size: size,
              scale: scale,
              platform: size.width < 600
                  ? TargetPlatform.android
                  : TargetPlatform.windows,
            );
            expect(tester.takeException(), isNull);
            if (size == const Size(360, 640) && scale == 1) {
              final action = find.byKey(
                ValueKey(logged ? 'connect-primary' : 'login-primary'),
              );
              expect(action.hitTestable(), findsOneWidget);
              expect(
                tester.getRect(action).bottom,
                lessThan(
                  tester
                      .getRect(find.byKey(const ValueKey('navigation-0')))
                      .top,
                ),
              );
              if (logged) {
                expect(
                  find.byKey(const ValueKey('node-selector')).hitTestable(),
                  findsOneWidget,
                );
              }
              expect(find.byType(Brand), findsOneWidget);
            }
            for (final page in [1, 2, 3, 0]) {
              await navigate(tester, page);
              expect(tester.takeException(), isNull, reason: 'page $page');
              final scroll = find.byKey(ValueKey('page-scroll-$page'));
              await tester.drag(scroll, const Offset(0, -1200));
              await tester.pump(const Duration(seconds: 1));
              expect(
                tester.takeException(),
                isNull,
                reason: 'scrolled page $page',
              );
            }
          },
        );
      }
    }
  }

  testWidgets(
    'initialization and authorization keep actions clear and cancelable',
    (tester) async {
      final gate = Completer<void>();
      final transport = PreviewTransport(loggedIn: false, startGate: gate);
      await mount(tester, transport);
      expect(find.text('正在准备客户端…'), findsOneWidget);
      expect(
        tester
            .widget<FilledButton>(find.byKey(const ValueKey('login-primary')))
            .onPressed,
        isNull,
      );
      gate.complete();
      await tester.pumpAndSettle();
      await tester.tap(find.text('在浏览器中登录'));
      await tester.pumpAndSettle();
      expect(find.text('等待浏览器授权'), findsOneWidget);
      await tester.tap(find.text('取消登录'));
      await tester.pumpAndSettle();
      expect(transport.requests.any((r) => r.method == 'cancelLogin'), isTrue);
      expect(find.text('在浏览器中登录'), findsOneWidget);
    },
  );

  testWidgets('service address dialog validates and preserves saved input', (
    tester,
  ) async {
    final transport = PreviewTransport(loggedIn: false);
    await mount(tester, transport, size: const Size(320, 640), scale: 2);
    await tester.ensureVisible(find.text('服务地址'));
    await tester.tap(find.text('服务地址'));
    await tester.pumpAndSettle();
    await tester.enterText(find.byType(TextField), 'bad');
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    expect(find.text('请输入完整的 HTTPS 服务地址'), findsOneWidget);
    await tester.enterText(find.byType(TextField), 'https://example.test');
    await tester.tap(find.text('保存'));
    await tester.pumpAndSettle();
    await tester.ensureVisible(find.byKey(const ValueKey('login-primary')));
    await tester.tap(find.byKey(const ValueKey('login-primary')));
    await tester.pumpAndSettle();
    expect(
      transport.requests
          .firstWhere((r) => r.method == 'login')
          .params['server'],
      'https://example.test',
    );
    expect(tester.takeException(), isNull);
  });

  for (final size in const [Size(320, 640), Size(1280, 720)]) {
    testWidgets(
      'node search, unavailable rows, selection and cancel at $size',
      (tester) async {
        final transport = PreviewTransport();
        await mount(tester, transport, size: size, scale: 2);
        await tester.tap(find.byKey(const ValueKey('node-selector')));
        await tester.pumpAndSettle();
        expect(
          find.byType(BottomSheet),
          size.width < 600 ? findsOneWidget : findsNothing,
        );
        final field = find.byKey(const ValueKey('node-search'));
        await tester.enterText(field, '不存在');
        await tester.pump();
        expect(find.text('没有匹配的节点，试试其他关键词。'), findsOneWidget);
        await tester.enterText(field, '维护');
        await tester.pump();
        final offline = tester.widget<InkWell>(
          find.byKey(const ValueKey('node-standard/offline')),
        );
        expect(offline.onTap, isNull);
        await tester.enterText(field, '新加坡');
        await tester.pump();
        if (size.width < 600) {
          tester.view.viewInsets = const FakeViewPadding(bottom: 260);
          await tester.pumpAndSettle();
          expect(tester.takeException(), isNull);
          tester.view.resetViewInsets();
          await tester.pumpAndSettle();
        }
        await tester.tap(find.byKey(const ValueKey('node-standard/singapore')));
        await tester.pumpAndSettle();
        expect(
          transport.requests
              .where((r) => r.method == 'node')
              .single
              .params['nodeId'],
          'singapore',
        );
        await tester.tap(find.byKey(const ValueKey('node-selector')));
        await tester.pumpAndSettle();
        await tester.tap(find.byTooltip('关闭节点选择'));
        await tester.pumpAndSettle();
        expect(transport.requests.where((r) => r.method == 'node').length, 1);
        expect(tester.takeException(), isNull);
      },
    );
  }

  testWidgets(
    'connection states distinguish actual subscription and retain disconnect',
    (tester) async {
      final transport = PreviewTransport();
      await mount(tester, transport);
      for (final state in ['connecting', 'connected', 'recovering', 'failed']) {
        transport.emit({'runState': state, 'connectionIntent': true});
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 250));
        expect(find.text('断开连接'), findsOneWidget);
        expect(tester.takeException(), isNull);
      }
      transport.emit({
        'runState': 'connected',
        'actualNodeId': 'tokyo',
        'actualSubscriptionId': 'travel',
      });
      await tester.pumpAndSettle();
      expect(find.text('正在切换节点'), findsOneWidget);
      expect(find.textContaining('日本 · 东京 02 · 备用订阅'), findsOneWidget);
      await tester.tap(find.text('断开连接'));
      await tester.pumpAndSettle();
      expect(transport.requests.any((r) => r.method == 'disconnect'), isTrue);
    },
  );

  testWidgets('empty, expired and long subscription content stays readable', (
    tester,
  ) async {
    final transport = PreviewTransport(overrides: {'subscriptions': []});
    await mount(tester, transport, size: const Size(320, 640), scale: 2);
    await navigate(tester, 1);
    expect(find.text('还没有订阅'), findsOneWidget);
    transport.emit({
      'subscriptions': [
        {
          'id': 'standard',
          'templateName': '这是一个很长的订阅名称用于验证中英文混合 Example Long Subscription',
          'expiresAt': '2020-01-01T00:00:00Z',
          'status': 'active',
          'quotaBytes': 1000,
          'usedBytes': 500,
        },
      ],
    });
    await tester.pumpAndSettle();
    expect(find.text('已到期'), findsOneWidget);
    expect(tester.takeException(), isNull);
  });

  testWidgets(
    'update progress, badge and confirmation preserve install boundary',
    (tester) async {
      final transport = PreviewTransport();
      await mount(tester, transport, size: const Size(320, 640), scale: 2);
      await navigate(tester, 2);
      for (final phase in [
        'checking',
        'downloading',
        'verifying',
        'error',
        'available',
      ]) {
        transport.emit({
          'updates': {'phase': phase, 'downloaded': 20, 'total': 100},
        });
        await tester.pump();
        await tester.pump(const Duration(milliseconds: 250));
        expect(tester.takeException(), isNull);
      }
      transport.emit({
        'updates': {
          'phase': 'ready',
          'ready': true,
          'version': '0.1.0-preview.3',
        },
      });
      await tester.pumpAndSettle();
      expect(
        tester
            .widgetList<Badge>(find.byType(Badge))
            .where((b) => b.isLabelVisible)
            .length,
        1,
      );
      final install = find.text('重启并更新');
      await tester.ensureVisible(install);
      await tester.tap(install);
      await tester.pumpAndSettle();
      await tester.tap(find.text('取消'));
      await tester.pumpAndSettle();
      expect(
        transport.requests.any((r) => r.method == 'installUpdate'),
        isFalse,
      );
      await tester.tap(install);
      await tester.pumpAndSettle();
      await tester.tap(
        find.descendant(
          of: find.byType(AlertDialog),
          matching: find.text('重启并更新'),
        ),
      );
      await tester.pumpAndSettle();
      expect(
        transport.requests.where((r) => r.method == 'installUpdate').length,
        1,
      );
      expect(tester.takeException(), isNull);
    },
  );

  test('traffic snapshots do not rebuild static view, but selection and updates do', () async {
    final transport = PreviewTransport();
    final container = ProviderContainer(
      overrides: [transportProvider.overrideWithValue(transport)],
    );
    addTearDown(container.dispose);
    var rebuilds = 0;
    container.listen(
      uiStateProvider,
      (_, _) => rebuilds++,
      fireImmediately: true,
    );
    await Future<void>.delayed(Duration.zero);
    await container.pump();
    expect(container.read(sessionProvider).ready, isTrue);
    final initial = rebuilds;
    transport.emit({
      'metrics': {'downloadRate': 512, 'available': true},
      'nodeTests': {'pending': true},
    });
    await Future<void>.delayed(Duration.zero);
    await container.pump();
    expect(rebuilds, initial);
    transport.emit({'desiredNodeId': 'singapore'});
    await Future<void>.delayed(Duration.zero);
    await container.pump();
    expect(rebuilds, initial + 1);
    transport.emit({
      'updates': {'ready': true},
    });
    await Future<void>.delayed(Duration.zero);
    await container.pump();
    expect(rebuilds, initial + 2);
  });

  testWidgets('reduced motion disables navigation transitions', (tester) async {
    tester.platformDispatcher.accessibilityFeaturesTestValue =
        const FakeAccessibilityFeatures(disableAnimations: true);
    addTearDown(tester.platformDispatcher.clearAccessibilityFeaturesTestValue);
    await mount(tester, PreviewTransport());
    final tile = find.descendant(
      of: find.byKey(const ValueKey('navigation-1')),
      matching: find.byType(AnimatedContainer),
    );
    expect(tester.widget<AnimatedContainer>(tile).duration, Duration.zero);
  });
}
