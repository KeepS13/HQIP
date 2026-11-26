// Optional visual evidence: flutter test test/preview_capture_test.dart
//   --dart-define=HQIP_CAPTURE_DIR=<absolute output folder>
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';
import 'package:flutter/rendering.dart';
import 'package:flutter/services.dart';
import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:hqip_preview/app.dart';
import 'package:hqip_preview/runtime/session.dart';

import '../tool/preview_fixture.dart';

void main() {
  const directory = String.fromEnvironment('HQIP_CAPTURE_DIR');
  testWidgets('capture desktop and Android layouts with offline fixture data', (
    tester,
  ) async {
    final font = File(r'C:\Windows\Fonts\msyh.ttc').readAsBytesSync();
    for (final family in ['Ahem', 'Segoe UI', 'Roboto', 'Microsoft YaHei UI']) {
      await (FontLoader(
        family,
      )..addFont(Future.value(ByteData.sublistView(font)))).load();
    }
    final icons = File(
      '../.local/flutter-sdk/bin/cache/artifacts/material_fonts/MaterialIcons-Regular.otf',
    ).readAsBytesSync();
    await (FontLoader(
      'MaterialIcons',
    )..addFont(Future.value(ByteData.sublistView(icons)))).load();
    final boundaryKey = GlobalKey();
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);
    addTearDown(tester.view.resetPadding);
    addTearDown(() => debugDefaultTargetPlatformOverride = null);
    Future<void> capture(String name) async {
      await tester.pump(const Duration(milliseconds: 300));
      expect(tester.takeException(), isNull);
      final boundary =
          boundaryKey.currentContext!.findRenderObject()!
              as RenderRepaintBoundary;
      await tester.runAsync(() async {
        final image = await boundary.toImage(pixelRatio: 1.5);
        final bytes = await image.toByteData(format: ui.ImageByteFormat.png);
        final file = File('$directory/$name.png');
        await file.parent.create(recursive: true);
        await file.writeAsBytes(bytes!.buffer.asUint8List());
        image.dispose();
      });
    }

    for (final mobile in [false, true]) {
      final prefix = mobile ? 'android' : 'windows';
      tester.view.physicalSize = mobile
          ? const Size(360, 640)
          : const Size(1280, 720);
      tester.view.devicePixelRatio = 1;
      tester.view.padding = mobile
          ? const FakeViewPadding(top: 24, bottom: 24)
          : const FakeViewPadding();
      debugDefaultTargetPlatformOverride = mobile
          ? TargetPlatform.android
          : TargetPlatform.windows;
      for (final logged in [false, true]) {
        await tester.pumpWidget(const SizedBox.shrink());
        final transport = PreviewTransport(
          loggedIn: logged,
          overrides: logged
              ? {
                  'runState': 'connected',
                  'connectionIntent': true,
                  'actualNodeId': 'tokyo',
                  'actualSubscriptionId': 'standard',
                  'metrics': {
                    'available': true,
                    'downloadRate': 2480000,
                    'uploadRate': 168000,
                    'downloadBytes': 164000000,
                    'uploadBytes': 28000000,
                    'delayMs': 38,
                    'history': [
                      for (var i = 0; i < 60; i++)
                        {
                          'downloadRate': 1200000 + ((i * 1973) % 2400) * 1000,
                          'uploadRate': 100000 + ((i * 113) % 600) * 1000,
                        },
                    ],
                  },
                  'updates': {
                    'phase': 'ready',
                    'ready': true,
                    'version': '0.1.0-preview.3',
                    'message': '下载完成，可以安装更新。',
                  },
                }
              : {},
        );
        await tester.pumpWidget(
          RepaintBoundary(
            key: boundaryKey,
            child: ProviderScope(
              overrides: [transportProvider.overrideWithValue(transport)],
              child: const HqipApp(),
            ),
          ),
        );
        await tester.pumpAndSettle();
        await capture('$prefix-${logged ? 'connection' : 'login'}');
        if (!logged) continue;
        for (final (index, name) in [
          (1, 'subscriptions'),
          (2, 'updates'),
          (3, 'settings'),
        ]) {
          await tester.tap(find.byKey(ValueKey('navigation-$index')));
          await tester.pumpAndSettle();
          await capture('$prefix-$name');
        }
        await tester.tap(find.byKey(const ValueKey('navigation-0')));
        await tester.pumpAndSettle();
        await tester.tap(find.byKey(const ValueKey('node-selector')));
        await tester.pumpAndSettle();
        await capture('$prefix-nodes');
      }
    }
  }, skip: directory.isEmpty);
}
