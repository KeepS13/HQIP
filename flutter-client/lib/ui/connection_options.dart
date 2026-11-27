import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';

class ConnectionOptions extends ConsumerWidget {
  const ConnectionOptions({super.key});

  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final state = app.snapshot;
    final locked =
        !app.ready ||
        app.draft != null ||
        state.selectionPending ||
        state.runState == 'connecting' ||
        app.busy.any(
          (value) =>
              ['mode', 'subscription', 'connect', 'disconnect'].contains(value),
        );
    final mobile = Theme.of(context).platform == TargetPlatform.android;
    void change(String mode, bool tun) {
      if (state.mode != mode || state.tun != tun) {
        ref.read(sessionProvider.notifier).command('mode', {
          'mode': mode,
          'tun': tun,
        });
      }
    }

    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Wrap(
          spacing: 8,
          runSpacing: 8,
          crossAxisAlignment: WrapCrossAlignment.center,
          children: [
            for (final mode in ['rule', 'global'])
              ChoiceChip(
                key: ValueKey('quick-mode-$mode'),
                label: Text(mode == 'rule' ? '规则模式' : '全局模式'),
                avatar: Icon(
                  mode == 'rule' ? Icons.route_outlined : Icons.public,
                  size: 16,
                ),
                selected: state.mode == mode,
                showCheckmark: false,
                selectedColor: selectedSurface,
                onSelected: locked
                    ? null
                    : (_) => change(mode, mobile || state.tun),
              ),
            if (mobile)
              const StatusPill('VPN', icon: Icons.vpn_lock_outlined)
            else
              Row(
                mainAxisSize: MainAxisSize.min,
                children: [
                  const Text(
                    'TUN',
                    style: TextStyle(color: muted, fontSize: 12),
                  ),
                  const SizedBox(width: 4),
                  Semantics(
                    label: 'TUN 虚拟网卡',
                    child: Switch(
                      key: const ValueKey('quick-tun'),
                      value: state.tun,
                      onChanged: locked
                          ? null
                          : (value) => change(state.mode, value),
                    ),
                  ),
                ],
              ),
          ],
        ),
        const SizedBox(height: 4),
        Text(
          app.busy.contains('mode')
              ? '正在切换连接方式…'
              : mobile
              ? '规则模式智能分流，全局模式让普通流量通过节点。'
              : state.tun
              ? 'TUN 接管系统流量；首次开启需要系统授权。'
              : '当前使用系统代理；TUN 可接管不支持系统代理的应用。',
          style: const TextStyle(color: muted, fontSize: 12),
        ),
      ],
    );
  }
}
