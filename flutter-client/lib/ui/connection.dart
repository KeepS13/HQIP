import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'login.dart';
import 'node_picker.dart';
import 'presentation.dart';
import 'subscription_card.dart';
import 'traffic.dart';

class ConnectionPage extends ConsumerWidget {
  const ConnectionPage({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final state = app.snapshot;
    if (!state.loggedIn) return const LoginPage();
    final sub = state.package(app.selected?.subscriptionId ?? '');
    final quota = sub == null
        ? const EmptyState(
            icon: Icons.layers_outlined,
            title: '还没有可用订阅',
            message: '在订阅页面查看套餐，或刷新账户同步订阅。',
          )
        : SubscriptionCard(subscription: sub, compact: true);
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        PageHeading(
          '连接',
          state.device,
          trailing: IconButton(
            tooltip: '刷新账户',
            onPressed: app.busy.contains('refresh')
                ? null
                : () => ref.read(sessionProvider.notifier).command('refresh'),
            icon: const Icon(Icons.refresh, size: 21),
          ),
        ),
        LayoutBuilder(
          builder: (context, bounds) => bounds.maxWidth >= 760
              ? Row(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Expanded(flex: 3, child: _ConnectionControl()),
                    const SizedBox(width: 16),
                    Expanded(flex: 2, child: quota),
                  ],
                )
              : Column(
                  crossAxisAlignment: CrossAxisAlignment.stretch,
                  children: [
                    const _ConnectionControl(),
                    const SizedBox(height: 16),
                    quota,
                  ],
                ),
        ),
        const SizedBox(height: 16),
        const TrafficPanel(),
      ],
    );
  }
}

class _ConnectionControl extends ConsumerWidget {
  const _ConnectionControl();
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final state = app.snapshot;
    final selected = app.selected;
    final sub = state.package(selected?.subscriptionId ?? '');
    final connected = state.runState == 'connected';
    final switching =
        state.intent &&
        state.actualId.isNotEmpty &&
        (state.actualId != selected?.id ||
            state.actualSubscription != selected?.subscriptionId);
    final saving = app.draft != null || state.selectionPending;
    final label = switching
        ? '正在切换节点'
        : switch (state.runState) {
            'connected' => '已连接',
            'connecting' => '正在连接',
            'recovering' => '正在恢复连接',
            'failed' => '连接未完成',
            _ => '尚未连接',
          };
    final detail = switching
        ? '连接建立后自动切换流量'
        : connected
        ? '连接由后台持续维护'
        : state.intent
        ? '正在建立安全连接'
        : '选择节点，开始连接';
    final actual = state.choice(state.actualId, state.actualSubscription);
    final color = connected && !switching ? good : accent;
    return SectionCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              AnimatedContainer(
                duration: motionDuration(context),
                width: 44,
                height: 44,
                decoration: BoxDecoration(
                  color: canvas,
                  borderRadius: BorderRadius.circular(12),
                ),
                child: Icon(
                  connected
                      ? Icons.verified_user_outlined
                      : Icons.shield_outlined,
                  color: color,
                  size: 25,
                ),
              ),
              const SizedBox(width: 12),
              Expanded(
                child: Semantics(
                  liveRegion: true,
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      AnimatedSwitcher(
                        duration: motionDuration(context),
                        child: Align(
                          key: ValueKey(label),
                          alignment: Alignment.centerLeft,
                          child: Text(
                            label,
                            style: TextStyle(
                              fontSize: 20,
                              fontWeight: FontWeight.w700,
                              color: color,
                            ),
                          ),
                        ),
                      ),
                      const SizedBox(height: 4),
                      Text(
                        detail,
                        style: const TextStyle(color: muted, fontSize: 12),
                      ),
                    ],
                  ),
                ),
              ),
            ],
          ),
          const SizedBox(height: 20),
          Material(
            color: canvas,
            shape: RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(12),
              side: const BorderSide(color: outline),
            ),
            child: InkWell(
              key: const ValueKey('node-selector'),
              borderRadius: BorderRadius.circular(12),
              onTap: () => showNodePicker(context),
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Row(
                  children: [
                    const Icon(Icons.language, size: 22, color: accent),
                    const SizedBox(width: 12),
                    Expanded(
                      child: Column(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Text(
                            selected?.name ?? '选择节点',
                            style: const TextStyle(fontWeight: FontWeight.w600),
                          ),
                          const SizedBox(height: 2),
                          Text(
                            sub?.name ?? '查看可用节点',
                            style: const TextStyle(color: muted, fontSize: 12),
                          ),
                        ],
                      ),
                    ),
                    const SizedBox(width: 8),
                    const Icon(Icons.unfold_more, size: 20, color: muted),
                  ],
                ),
              ),
            ),
          ),
          if (saving)
            const Padding(
              padding: EdgeInsets.only(top: 8),
              child: Text(
                '正在保存节点选择…',
                style: TextStyle(color: accent, fontSize: 12),
              ),
            ),
          const SizedBox(height: 16),
          FilledButton.icon(
            key: const ValueKey('connect-primary'),
            style: FilledButton.styleFrom(
              backgroundColor: connected ? good : accent,
              foregroundColor: canvas,
            ),
            onPressed:
                !app.ready ||
                    app.busy.contains('disconnect') ||
                    (!state.intent &&
                        (selected == null ||
                            !selected.ready ||
                            !(sub?.valid ?? false) ||
                            saving ||
                            app.busy.contains('connect')))
                ? null
                : () => ref
                      .read(sessionProvider.notifier)
                      .command(state.intent ? 'disconnect' : 'connect'),
            icon: const Icon(Icons.power_settings_new, size: 20),
            label: Text(
              app.busy.contains('disconnect')
                  ? '正在断开…'
                  : state.intent
                  ? '断开连接'
                  : '连接网络',
            ),
          ),
          const SizedBox(height: 12),
          Wrap(
            spacing: 8,
            runSpacing: 8,
            children: [
              StatusPill(
                state.mode == 'global' ? '全局模式' : '规则模式',
                icon: Icons.route_outlined,
              ),
              StatusPill(
                (Theme.of(context).platform == TargetPlatform.android)
                    ? 'VPN'
                    : state.tun
                    ? 'TUN 虚拟网卡'
                    : '系统代理',
                icon: (Theme.of(context).platform == TargetPlatform.android)
                    ? Icons.vpn_lock_outlined
                    : Icons.desktop_windows_outlined,
              ),
            ],
          ),
          if (switching)
            Padding(
              padding: const EdgeInsets.only(top: 12),
              child: Text(
                '当前流量仍经由：${actual?.name ?? state.actualId} · ${state.package(state.actualSubscription)?.name ?? '原订阅'}',
                style: const TextStyle(color: muted, fontSize: 12),
              ),
            ),
          if (state.error.isNotEmpty)
            Padding(
              padding: const EdgeInsets.only(top: 12),
              child: Text(
                state.error,
                style: TextStyle(
                  color: Theme.of(context).colorScheme.error,
                  fontSize: 12,
                ),
              ),
            ),
        ],
      ),
    );
  }
}
