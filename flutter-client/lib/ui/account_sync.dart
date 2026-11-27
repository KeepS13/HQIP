import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';

String localDateTime(dynamic value) {
  final time = value is String ? DateTime.tryParse(value)?.toLocal() : null;
  if (time == null) return '尚未同步';
  String two(int value) => value.toString().padLeft(2, '0');
  return '${time.year}/${two(time.month)}/${two(time.day)} ${two(time.hour)}:${two(time.minute)}:${two(time.second)}';
}

class AccountSync extends ConsumerWidget {
  const AccountSync({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final raw = app.snapshot.raw;
    final unavailable = raw['controlUnavailable'] == true;
    final offlineUntil = raw['offlineUntil'] as String? ?? '';
    final connectionMessage = raw['connections'] is Map
        ? raw['connections']['message'] as String? ?? ''
        : '';
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        Wrap(
          spacing: 12,
          runSpacing: 4,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            StatusPill(
              unavailable ? '账户暂未同步' : '账户同步',
              icon: Icons.sync,
              color: unavailable ? muted : good,
            ),
            Text(
              localDateTime(raw['lastSync']),
              style: const TextStyle(color: muted, fontSize: 12),
            ),
            TextButton.icon(
              key: const ValueKey('sync-account'),
              onPressed: !app.ready || app.busy.contains('refresh')
                  ? null
                  : () => ref.read(sessionProvider.notifier).command('refresh'),
              icon: const Icon(Icons.refresh, size: 16),
              label: Text(app.busy.contains('refresh') ? '同步中…' : '刷新账户'),
            ),
          ],
        ),
        if (unavailable && offlineUntil.isNotEmpty)
          Text(
            '当前离线授权有效至 ${localDateTime(offlineUntil)}',
            style: const TextStyle(color: muted, fontSize: 12),
          ),
        if (connectionMessage.isNotEmpty)
          Text(
            connectionMessage,
            style: const TextStyle(color: muted, fontSize: 12),
          ),
      ],
    );
  }
}
