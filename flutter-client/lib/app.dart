import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'runtime/session.dart';
import 'ui/design.dart';
import 'ui/connection.dart';
import 'ui/subscriptions.dart';
import 'ui/updates.dart';
import 'ui/settings.dart';

class HqipApp extends StatelessWidget {
  const HqipApp({super.key});
  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'HQIP Preview',
    debugShowCheckedModeBanner: false,
    theme: appTheme(),
    home: const AppShell(),
  );
}

class AppShell extends ConsumerStatefulWidget {
  const AppShell({super.key});
  @override
  ConsumerState<AppShell> createState() => _AppShellState();
}

class _AppShellState extends ConsumerState<AppShell>
    with WidgetsBindingObserver {
  int _page = 0;
  static const titles = ['连接', '订阅', '更新', '设置'];
  static const icons = [
    Icons.shield_outlined,
    Icons.layers_outlined,
    Icons.system_update_alt,
    Icons.tune,
  ];
  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    ref
        .read(sessionProvider.notifier)
        .visibility(state == AppLifecycleState.resumed);
  }

  @override
  Widget build(BuildContext context) {
    ref.listen<bool>(sessionProvider.select((s) => s.snapshot.loggedIn), (
      previous,
      next,
    ) {
      if (previous == false && next && mounted) setState(() => _page = 0);
    });
    final logged = ref.watch(
      sessionProvider.select((s) => s.snapshot.loggedIn),
    );
    final updateReady = ref.watch(
      sessionProvider.select((s) => s.snapshot.updates['ready'] == true),
    );
    return LayoutBuilder(
      builder: (context, bounds) {
        final wide = bounds.maxWidth >= 900;
        final page = switch (_page) {
          1 => const SubscriptionsPage(),
          2 => const UpdatesPage(),
          3 => const SettingsPage(),
          _ => const ConnectionPage(),
        };
        Widget destination(int i, {bool compact = false}) => _Destination(
          key: ValueKey('navigation-$i'),
          title: titles[i],
          icon: icons[i],
          selected: i == _page,
          badge: i == 2 && updateReady,
          compact: compact,
          onTap: () => setState(() => _page = i),
        );
        return Scaffold(
          appBar: wide
              ? null
              : AppBar(
                  title: const Brand(),
                  toolbarHeight: 56,
                  titleSpacing: 16,
                ),
          bottomNavigationBar: wide
              ? null
              : Material(
                  color: sidebar,
                  child: SafeArea(
                    top: false,
                    child: Container(
                      decoration: const BoxDecoration(
                        border: Border(top: BorderSide(color: outline)),
                      ),
                      padding: const EdgeInsets.symmetric(
                        horizontal: 8,
                        vertical: 6,
                      ),
                      child: Row(
                        crossAxisAlignment: CrossAxisAlignment.start,
                        children: [
                          for (var i = 0; i < titles.length; i++)
                            Expanded(child: destination(i, compact: true)),
                        ],
                      ),
                    ),
                  ),
                ),
          body: SafeArea(
            bottom: wide,
            child: Row(
              children: [
                if (wide)
                  SizedBox(
                    width: 208,
                    child: Material(
                      color: sidebar,
                      child: LayoutBuilder(
                        builder: (context, sidebarBounds) => SingleChildScrollView(
                          child: ConstrainedBox(
                            constraints: BoxConstraints(
                              minHeight: sidebarBounds.maxHeight,
                            ),
                            child: IntrinsicHeight(
                              child: Padding(
                                padding: const EdgeInsets.fromLTRB(
                                  16,
                                  28,
                                  16,
                                  16,
                                ),
                                child: Column(
                                  crossAxisAlignment:
                                      CrossAxisAlignment.stretch,
                                  children: [
                                    const Padding(
                                      padding: EdgeInsets.only(left: 8),
                                      child: Brand(),
                                    ),
                                    const SizedBox(height: 36),
                                    for (var i = 0; i < titles.length; i++)
                                      Padding(
                                        padding: const EdgeInsets.only(
                                          bottom: 8,
                                        ),
                                        child: destination(i),
                                      ),
                                    const Spacer(),
                                    const SizedBox(height: 24),
                                    const Divider(),
                                    const SizedBox(height: 8),
                                    InkWell(
                                      borderRadius: BorderRadius.circular(12),
                                      onTap: () => setState(() => _page = 3),
                                      child: Padding(
                                        padding: const EdgeInsets.all(8),
                                        child: Row(
                                          children: [
                                            Icon(
                                              logged
                                                  ? Icons.verified_user_outlined
                                                  : Icons.person_outline,
                                              size: 20,
                                              color: muted,
                                            ),
                                            const SizedBox(width: 10),
                                            Expanded(
                                              child: Text(
                                                logged ? '账户已登录' : '账户未登录',
                                                style: const TextStyle(
                                                  color: muted,
                                                  fontSize: 12,
                                                ),
                                              ),
                                            ),
                                          ],
                                        ),
                                      ),
                                    ),
                                    const Padding(
                                      padding: EdgeInsets.fromLTRB(8, 8, 8, 0),
                                      child: Text(
                                        '独立预览版',
                                        style: TextStyle(
                                          color: muted,
                                          fontSize: 12,
                                        ),
                                      ),
                                    ),
                                  ],
                                ),
                              ),
                            ),
                          ),
                        ),
                      ),
                    ),
                  ),
                Expanded(
                  child: LayoutBuilder(
                    builder: (context, contentBounds) {
                      final padding = wide ? 32.0 : 16.0;
                      return Align(
                        alignment: Alignment.topCenter,
                        child: ConstrainedBox(
                          constraints: const BoxConstraints(maxWidth: 1160),
                          child: SingleChildScrollView(
                            key: ValueKey('page-scroll-$_page'),
                            padding: EdgeInsets.all(padding),
                            child: Column(
                              crossAxisAlignment: CrossAxisAlignment.stretch,
                              children: [
                                const _RuntimeError(),
                                if (_page == 0 && !logged)
                                  ConstrainedBox(
                                    constraints: BoxConstraints(
                                      minHeight:
                                          (contentBounds.maxHeight -
                                                  padding * 2)
                                              .clamp(0, double.infinity),
                                    ),
                                    child: page,
                                  )
                                else
                                  page,
                              ],
                            ),
                          ),
                        ),
                      );
                    },
                  ),
                ),
              ],
            ),
          ),
        );
      },
    );
  }
}

class _Destination extends StatelessWidget {
  const _Destination({
    super.key,
    required this.title,
    required this.icon,
    required this.selected,
    required this.onTap,
    this.compact = false,
    this.badge = false,
  });
  final String title;
  final IconData icon;
  final bool selected, compact, badge;
  final VoidCallback onTap;
  @override
  Widget build(BuildContext context) {
    final color = selected ? accent : muted;
    final symbol = Badge(
      isLabelVisible: badge,
      backgroundColor: accent,
      child: Icon(icon, size: 22, color: color),
    );
    final label = Text(
      title,
      style: TextStyle(
        color: color,
        fontSize: compact ? 12 : 14,
        fontWeight: selected ? FontWeight.w600 : FontWeight.w400,
      ),
    );
    return Semantics(
      selected: selected,
      button: true,
      label: badge ? '$title，有更新可安装' : title,
      excludeSemantics: true,
      child: AnimatedContainer(
        duration: motionDuration(context),
        decoration: BoxDecoration(
          color: selected ? selectedSurface : Colors.transparent,
          borderRadius: BorderRadius.circular(12),
        ),
        child: InkWell(
          onTap: onTap,
          borderRadius: BorderRadius.circular(12),
          child: Padding(
            padding: EdgeInsets.symmetric(
              horizontal: compact ? 4 : 14,
              vertical: compact ? 6 : 14,
            ),
            child: compact
                ? Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [symbol, const SizedBox(height: 4), label],
                  )
                : Row(
                    children: [
                      symbol,
                      const SizedBox(width: 12),
                      Expanded(child: label),
                    ],
                  ),
          ),
        ),
      ),
    );
  }
}

class _RuntimeError extends ConsumerWidget {
  const _RuntimeError();
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final error = ref.watch(sessionProvider.select((s) => s.error));
    final loggedIn = ref.watch(
      sessionProvider.select((s) => s.snapshot.loggedIn),
    );
    final canRetry = ref.watch(
      sessionProvider.select(
        (s) =>
            s.ready &&
            !s.snapshot.loginPending &&
            !s.busy.contains('login') &&
            !s.busy.contains('refresh'),
      ),
    );
    if (error.isEmpty) return const SizedBox.shrink();
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: SectionCard(
        padding: 16,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            const SectionTitle('操作暂未完成', icon: Icons.info_outline),
            const SizedBox(height: 8),
            Text(
              error,
              style: TextStyle(color: Theme.of(context).colorScheme.error),
            ),
            TextButton.icon(
              onPressed: canRetry
                  ? () => ref.read(sessionProvider.notifier).retryAccount()
                  : null,
              icon: const Icon(Icons.refresh, size: 18),
              label: Text(loggedIn ? '重新同步' : '重新登录'),
            ),
          ],
        ),
      ),
    );
  }
}
