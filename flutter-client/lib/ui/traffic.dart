import 'dart:math' as math;

import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/models.dart';
import '../runtime/session.dart';
import 'design.dart';

class TrafficPanel extends ConsumerWidget {
  const TrafficPanel({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(sessionProvider);
    final metrics = app.snapshot.metrics;
    final connected = app.snapshot.runState == 'connected';
    final live =
        connected && metrics['available'] == true && metrics['stale'] != true;
    final points = objects(metrics['history']);
    final history = points.length > 60
        ? points.sublist(points.length - 60)
        : points;
    final current = app.selected;
    final delayPending =
        metrics['delayPending'] == true || app.busy.contains('delay');
    return SectionCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          SectionTitle(
            '实时流量',
            trailing: StatusPill(
              live
                  ? '实时'
                  : connected
                  ? '等待数据'
                  : '未连接',
              color: live ? good : muted,
            ),
          ),
          const SizedBox(height: 20),
          LayoutBuilder(
            builder: (context, bounds) {
              final columns =
                  (bounds.maxWidth /
                          (140 * MediaQuery.textScalerOf(context).scale(1)))
                      .floor()
                      .clamp(1, 3);
              final width = (bounds.maxWidth - (columns - 1) * 16) / columns;
              return Wrap(
                spacing: 16,
                runSpacing: 16,
                children: [
                  SizedBox(
                    width: width,
                    child: metric(
                      '下载速度',
                      live
                          ? '${bytes(number(metrics['downloadRate']))}/s'
                          : '—',
                      good,
                    ),
                  ),
                  SizedBox(
                    width: width,
                    child: metric(
                      '上传速度',
                      live ? '${bytes(number(metrics['uploadRate']))}/s' : '—',
                      accent,
                    ),
                  ),
                  SizedBox(
                    width: width,
                    child: metric(
                      '本次传输',
                      bytes(
                        number(metrics['downloadBytes']) +
                            number(metrics['uploadBytes']),
                      ),
                      muted,
                    ),
                  ),
                ],
              );
            },
          ),
          const SizedBox(height: 20),
          SizedBox(
            height: 100,
            width: double.infinity,
            child: RepaintBoundary(
              child: CustomPaint(painter: TrafficPainter(history)),
            ),
          ),
          const SizedBox(height: 8),
          const Text(
            '最近 60 秒 · 本机流量统计',
            style: TextStyle(fontSize: 12, color: muted),
          ),
          const SizedBox(height: 16),
          const Divider(height: 1),
          const SizedBox(height: 12),
          Wrap(
            spacing: 8,
            runSpacing: 4,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              Text(
                connected &&
                        app.snapshot.actualId == current?.id &&
                        app.snapshot.actualSubscription ==
                            current?.subscriptionId &&
                        metrics['delayMs'] is num
                    ? '连接延迟 ${number(metrics['delayMs']).toInt()} ms'
                    : '连接延迟 —',
                style: const TextStyle(color: muted, fontSize: 12),
              ),
              TextButton(
                onPressed: !connected || delayPending
                    ? null
                    : () => ref.read(sessionProvider.notifier).command('delay'),
                child: Text(delayPending ? '检测中…' : '检测延迟'),
              ),
              if (app.snapshot.nodeTests['pending'] == true)
                TextButton(
                  onPressed: () =>
                      ref.read(sessionProvider.notifier).command('cancelTest'),
                  child: const Text('取消节点检测'),
                )
              else
                TextButton(
                  onPressed: current == null || app.draft != null
                      ? null
                      : () => ref.read(sessionProvider.notifier).command(
                          'testNode',
                          {
                            'nodeId': current.id,
                            'subscriptionId': current.subscriptionId,
                          },
                        ),
                  child: const Text('检测节点'),
                ),
            ],
          ),
          const Text(
            '订阅用量以服务端结算为准。',
            style: TextStyle(fontSize: 12, color: muted),
          ),
        ],
      ),
    );
  }

  Widget metric(String title, String value, Color color) => Column(
    crossAxisAlignment: CrossAxisAlignment.start,
    children: [
      Text(title, style: const TextStyle(color: muted, fontSize: 12)),
      const SizedBox(height: 4),
      Text(
        value,
        style: TextStyle(
          color: color,
          fontSize: 22,
          fontWeight: FontWeight.w600,
        ),
      ),
    ],
  );
}

class TrafficPainter extends CustomPainter {
  TrafficPainter(this.points);
  final List<Json> points;
  @override
  void paint(Canvas canvas, Size size) {
    final grid = Paint()
      ..color = const Color(0xff30364a)
      ..strokeWidth = 1;
    for (final fraction in [.1, .5, .9]) {
      canvas.drawLine(
        Offset(0, size.height * fraction),
        Offset(size.width, size.height * fraction),
        grid,
      );
    }
    if (points.isEmpty) return;
    var peak = 1024.0;
    for (final p in points) {
      peak = math.max(
        peak,
        math.max(
          number(p['uploadRate']).toDouble(),
          number(p['downloadRate']).toDouble(),
        ),
      );
    }
    for (final field in ['uploadRate', 'downloadRate']) {
      final path = Path();
      for (var i = 0; i < points.length; i++) {
        final x = points.length == 1
            ? size.width
            : i / (points.length - 1) * size.width;
        final y = size.height * (.9 - .8 * number(points[i][field]) / peak);
        if (i == 0) {
          path.moveTo(x, y);
        } else {
          path.lineTo(x, y);
        }
      }
      canvas.drawPath(
        path,
        Paint()
          ..color = (field == 'uploadRate' ? accent : good)
          ..style = PaintingStyle.stroke
          ..strokeWidth = 2,
      );
    }
  }

  @override
  bool shouldRepaint(covariant TrafficPainter oldDelegate) =>
      oldDelegate.points.toString() != points.toString();
}
