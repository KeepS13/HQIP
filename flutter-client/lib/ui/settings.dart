import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';

class SettingsPage extends ConsumerWidget {
  const SettingsPage({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final state = app.snapshot;
    final session = ref.read(sessionProvider.notifier);
    final modeBusy = !app.ready || app.busy.contains('mode');
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const PageHeading('设置', '连接偏好与账户管理'),
        _SettingsGroup(
          title: '连接',
          icon: Icons.tune,
          children: [
            const Text('流量路由', style: TextStyle(fontWeight: FontWeight.w600)),
            const SizedBox(height: 12),
            LayoutBuilder(
              builder: (context, bounds) {
                final vertical =
                    bounds.maxWidth < 360 ||
                    MediaQuery.textScalerOf(context).scale(14) > 21;
                final options = [
                  for (final mode in ['rule', 'global'])
                    _RouteOption(
                      title: mode == 'rule' ? '规则模式' : '全局模式',
                      description: mode == 'rule' ? '按访问目标智能分流' : '普通流量通过所选节点',
                      icon: mode == 'rule'
                          ? Icons.route_outlined
                          : Icons.public,
                      selected:
                          (state.mode == 'global' ? 'global' : 'rule') == mode,
                      onTap: modeBusy
                          ? null
                          : () => session.command('mode', {
                              'mode': mode,
                              'tun': state.tun,
                            }),
                    ),
                ];
                return vertical
                    ? Column(
                        children: [
                          options[0],
                          const SizedBox(height: 8),
                          options[1],
                        ],
                      )
                    : Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          Expanded(child: options[0]),
                          const SizedBox(width: 12),
                          Expanded(child: options[1]),
                        ],
                      );
              },
            ),
            if ((Theme.of(context).platform == TargetPlatform.windows)) ...[
              const Divider(height: 32),
              _SettingRow(
                title: 'TUN 虚拟网卡',
                description: '首次开启需要 Windows 系统授权',
                trailing: Switch(
                  value: state.tun,
                  onChanged: modeBusy
                      ? null
                      : (value) => session.command('mode', {
                          'mode': state.mode,
                          'tun': value,
                        }),
                ),
              ),
            ],
          ],
        ),
        if ((Theme.of(context).platform == TargetPlatform.windows)) ...[
          const SizedBox(height: 16),
          _SettingsGroup(
            title: '启动与后台',
            icon: Icons.desktop_windows_outlined,
            children: [
              _SettingRow(
                title: '登录 Windows 后启动',
                description: '启动后台，不自动连接 VPN',
                trailing: Switch(
                  value: state.autoStart['enabled'] == true,
                  onChanged:
                      !app.ready ||
                          state.autoStart['supported'] != true ||
                          app.busy.contains('autoStart')
                      ? null
                      : (enabled) =>
                            session.command('autoStart', {'enabled': enabled}),
                ),
              ),
              const Divider(height: 32),
              const Text(
                '关闭窗口时释放界面内存，连接与托盘继续运行。',
                style: TextStyle(color: muted, fontSize: 12),
              ),
            ],
          ),
        ],
        const SizedBox(height: 16),
        _SettingsGroup(
          title: '账户',
          icon: Icons.person_outline,
          children: [
            DetailRow(
              '登录状态',
              state.loggedIn ? '已登录' : '未登录',
              color: state.loggedIn ? good : muted,
            ),
            if (state.loggedIn) DetailRow('当前设备', state.device),
            DetailRow('服务地址', state.server),
            if (state.loggedIn) ...[
              const SizedBox(height: 8),
              Align(
                alignment: Alignment.centerLeft,
                child: OutlinedButton.icon(
                  onPressed: () => session.command('account'),
                  icon: const Icon(Icons.open_in_new, size: 18),
                  label: const Text('管理账户'),
                ),
              ),
            ],
          ],
        ),
        const SizedBox(height: 16),
        _SettingsGroup(
          title: '关于',
          icon: Icons.info_outline,
          children: [
            DetailRow('HQIP Preview', state.version),
            const SizedBox(height: 8),
            const Text(
              '独立预览版，账户与设置单独保存，不影响已安装的正式客户端。',
              style: TextStyle(color: muted, fontSize: 12),
            ),
            const SizedBox(height: 8),
            Align(
              alignment: Alignment.centerLeft,
              child: TextButton.icon(
                onPressed: () => showLicensePage(
                  context: context,
                  applicationName: 'HQIP Preview',
                  applicationVersion: state.version,
                ),
                icon: const Icon(Icons.description_outlined, size: 18),
                label: const Text('开源许可'),
              ),
            ),
          ],
        ),
        if (state.loggedIn ||
            (Theme.of(context).platform == TargetPlatform.windows)) ...[
          const SizedBox(height: 24),
          Wrap(
            spacing: 12,
            runSpacing: 8,
            children: [
              if (state.loggedIn)
                OutlinedButton.icon(
                  onPressed: app.busy.contains('logout')
                      ? null
                      : () async {
                          if (await confirm(
                            context,
                            '退出账户？',
                            '将断开当前连接，并清除预览版保存的登录状态。',
                            '退出账户',
                          )) {
                            session.command('logout');
                          }
                        },
                  icon: const Icon(Icons.logout, size: 18),
                  label: const Text('退出账户'),
                ),
              if ((Theme.of(context).platform == TargetPlatform.windows))
                TextButton.icon(
                  onPressed: app.busy.contains('quitApp')
                      ? null
                      : () async {
                          if (await confirm(
                            context,
                            '完全退出客户端？',
                            '将断开 VPN、恢复网络设置并退出托盘。',
                            '退出',
                          )) {
                            session.command('quitApp');
                          }
                        },
                  icon: const Icon(Icons.power_settings_new, size: 18),
                  label: const Text('退出并恢复网络'),
                ),
            ],
          ),
        ],
      ],
    );
  }
}

class _SettingsGroup extends StatelessWidget {
  const _SettingsGroup({
    required this.title,
    required this.icon,
    required this.children,
  });
  final String title;
  final IconData icon;
  final List<Widget> children;
  @override
  Widget build(BuildContext context) => SectionCard(
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        SectionTitle(title, icon: icon),
        const SizedBox(height: 20),
        ...children,
      ],
    ),
  );
}

class _SettingRow extends StatelessWidget {
  const _SettingRow({
    required this.title,
    required this.description,
    required this.trailing,
  });
  final String title, description;
  final Widget trailing;
  @override
  Widget build(BuildContext context) => LayoutBuilder(
    builder: (context, bounds) {
      final text = Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Text(title, style: const TextStyle(fontWeight: FontWeight.w600)),
          const SizedBox(height: 4),
          Text(description, style: const TextStyle(color: muted, fontSize: 12)),
        ],
      );
      return bounds.maxWidth < 320 &&
              MediaQuery.textScalerOf(context).scale(14) > 21
          ? Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                text,
                Align(alignment: Alignment.centerRight, child: trailing),
              ],
            )
          : Row(
              children: [
                Expanded(child: text),
                const SizedBox(width: 12),
                trailing,
              ],
            );
    },
  );
}

class _RouteOption extends StatelessWidget {
  const _RouteOption({
    required this.title,
    required this.description,
    required this.icon,
    required this.selected,
    this.onTap,
  });
  final String title, description;
  final IconData icon;
  final bool selected;
  final VoidCallback? onTap;
  @override
  Widget build(BuildContext context) => Semantics(
    selected: selected,
    button: true,
    child: AnimatedContainer(
      duration: motionDuration(context),
      decoration: BoxDecoration(
        color: selected ? selectedSurface : canvas,
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: selected ? accent : outline),
      ),
      child: InkWell(
        onTap: onTap,
        borderRadius: BorderRadius.circular(12),
        child: Padding(
          padding: const EdgeInsets.all(12),
          child: Row(
            children: [
              Icon(icon, color: selected ? accent : muted, size: 20),
              const SizedBox(width: 12),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      title,
                      style: TextStyle(
                        color: selected ? accent : null,
                        fontWeight: FontWeight.w600,
                      ),
                    ),
                    const SizedBox(height: 4),
                    Text(
                      description,
                      style: const TextStyle(color: muted, fontSize: 12),
                    ),
                  ],
                ),
              ),
              const SizedBox(width: 8),
              Icon(
                selected ? Icons.check_circle_outline : Icons.circle_outlined,
                size: 18,
                color: selected ? accent : muted,
              ),
            ],
          ),
        ),
      ),
    ),
  );
}
