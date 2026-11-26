import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/models.dart';
import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';

class UpdatesPage extends ConsumerWidget {
  const UpdatesPage({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final update = app.snapshot.updates;
    final phase = update['phase'] as String? ?? 'idle';
    final busy =
        ['checking', 'downloading', 'verifying'].contains(phase) ||
        app.busy.contains('downloadUpdate');
    final ready = update['ready'] == true;
    final total = number(update['total']);
    final received = number(update['downloaded']);
    final title = ready
        ? '更新已准备好'
        : switch (phase) {
            'checking' => '正在检查更新',
            'downloading' => '正在下载更新',
            'verifying' => '正在验证更新',
            'error' => '更新暂未完成',
            'available' => '发现新版本',
            _ => '检查应用更新',
          };
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const PageHeading('应用更新', '自动发现新版本，安装时间由你决定'),
        SectionCard(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Container(
                    width: 44,
                    height: 44,
                    decoration: BoxDecoration(
                      color: canvas,
                      borderRadius: BorderRadius.circular(12),
                    ),
                    child: Icon(
                      ready ? Icons.task_alt : Icons.system_update_alt,
                      color: ready ? good : accent,
                      size: 24,
                    ),
                  ),
                  const SizedBox(width: 12),
                  Expanded(
                    child: Semantics(
                      liveRegion: true,
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            title,
                            style: Theme.of(context).textTheme.titleLarge,
                          ),
                          const SizedBox(height: 4),
                          const Text(
                            '预览渠道',
                            style: TextStyle(color: muted, fontSize: 12),
                          ),
                        ],
                      ),
                    ),
                  ),
                ],
              ),
              const SizedBox(height: 20),
              const Divider(height: 1),
              const SizedBox(height: 12),
              DetailRow('当前版本', app.snapshot.version),
              if ((update['version'] as String? ?? '').isNotEmpty)
                DetailRow('可用版本', update['version'] as String, color: good),
              const SizedBox(height: 12),
              Text(
                (update['message'] as String? ?? '').isEmpty
                    ? '有新版本时会在这里提示。'
                    : update['message'] as String,
                style: const TextStyle(color: muted),
              ),
              if (busy) ...[
                const SizedBox(height: 20),
                LinearProgressIndicator(
                  value: phase == 'downloading' && total > 0
                      ? (received / total).clamp(0, 1).toDouble()
                      : null,
                  minHeight: 4,
                  borderRadius: BorderRadius.circular(4),
                ),
                const SizedBox(height: 8),
                if (phase == 'downloading')
                  Text(
                    total > 0
                        ? '${bytes(received)} / ${bytes(total)} · ${(received / total * 100).clamp(0, 100).toInt()}%'
                        : '已下载 ${bytes(received)}',
                    style: const TextStyle(color: muted, fontSize: 12),
                  ),
              ],
              const SizedBox(height: 24),
              Wrap(
                spacing: 12,
                runSpacing: 8,
                children: [
                  if (ready)
                    FilledButton.icon(
                      onPressed:
                          !app.ready || app.busy.contains('installUpdate')
                          ? null
                          : () async {
                              final yes = await confirm(
                                context,
                                (Theme.of(context).platform ==
                                        TargetPlatform.windows)
                                    ? '重启并更新？'
                                    : '安装新版本？',
                                '当前连接会在安装过程中断开。你也可以稍后更新。',
                                (Theme.of(context).platform ==
                                        TargetPlatform.windows)
                                    ? '重启并更新'
                                    : '继续安装',
                              );
                              if (yes) {
                                ref
                                    .read(sessionProvider.notifier)
                                    .command('installUpdate');
                              }
                            },
                      icon: const Icon(Icons.restart_alt, size: 18),
                      label: Text(
                        (Theme.of(context).platform == TargetPlatform.windows)
                            ? '重启并更新'
                            : '安装更新',
                      ),
                    ),
                  if (ready)
                    OutlinedButton.icon(
                      onPressed: busy || !app.ready
                          ? null
                          : () => ref
                                .read(sessionProvider.notifier)
                                .command('downloadUpdate'),
                      icon: const Icon(Icons.refresh, size: 18),
                      label: const Text('检查更新'),
                    )
                  else
                    FilledButton.icon(
                      onPressed: busy || !app.ready
                          ? null
                          : () => ref
                                .read(sessionProvider.notifier)
                                .command('downloadUpdate'),
                      icon: Icon(
                        phase == 'available'
                            ? Icons.download_outlined
                            : Icons.refresh,
                        size: 18,
                      ),
                      label: Text(
                        busy
                            ? '正在处理…'
                            : phase == 'available'
                            ? '立即下载'
                            : phase == 'error'
                            ? '重新检查'
                            : '检查更新',
                      ),
                    ),
                ],
              ),
            ],
          ),
        ),
        const SizedBox(height: 16),
        SectionCard(
          child: Column(
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              const SectionTitle('更新方式', icon: Icons.info_outline),
              const SizedBox(height: 12),
              Text(
                (Theme.of(context).platform == TargetPlatform.windows)
                    ? '后台下载不打断连接。准备完成后，点击“重启并更新”安装；启动异常时会恢复上一版本。'
                    : '默认通过 Wi-Fi 自动下载。安装由 Android 系统确认，账户与设置会保留。',
                style: const TextStyle(color: muted),
              ),
              const SizedBox(height: 12),
              const Text(
                '此渠道仅更新 HQIP 预览版。',
                style: TextStyle(color: muted, fontSize: 12),
              ),
            ],
          ),
        ),
      ],
    );
  }
}
