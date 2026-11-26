import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';
import 'subscription_card.dart';

class SubscriptionsPage extends ConsumerWidget {
  const SubscriptionsPage({super.key});
  @override
  Widget build(BuildContext context, WidgetRef ref) {
    final app = ref.watch(uiStateProvider);
    final items = app.snapshot.subscriptions;
    final logged = app.snapshot.loggedIn;
    return Column(
      crossAxisAlignment: CrossAxisAlignment.stretch,
      children: [
        const PageHeading('我的订阅', '查看流量余额、有效期与连接名额'),
        if (!logged)
          const EmptyState(
            icon: Icons.person_outline,
            title: '登录后查看订阅',
            message: '前往连接页面登录，订阅将自动同步。',
          )
        else if (items.isEmpty)
          const EmptyState(
            icon: Icons.layers_outlined,
            title: '还没有订阅',
            message: '已有套餐可刷新账户同步，也可以前往商城选购。',
          )
        else
          LayoutBuilder(
            builder: (context, bounds) => Wrap(
              spacing: 16,
              runSpacing: 16,
              children: [
                for (final item in items)
                  SizedBox(
                    width: bounds.maxWidth >= 680
                        ? (bounds.maxWidth - 16) / 2
                        : bounds.maxWidth,
                    child: SubscriptionCard(subscription: item),
                  ),
              ],
            ),
          ),
        if (logged) ...[
          const SizedBox(height: 16),
          Wrap(
            spacing: 12,
            runSpacing: 8,
            crossAxisAlignment: WrapCrossAlignment.center,
            children: [
              OutlinedButton.icon(
                onPressed: () =>
                    ref.read(sessionProvider.notifier).command('shop'),
                icon: const Icon(Icons.open_in_new, size: 18),
                label: const Text('订阅商城'),
              ),
              TextButton.icon(
                onPressed: app.busy.contains('refresh')
                    ? null
                    : () =>
                          ref.read(sessionProvider.notifier).command('refresh'),
                icon: const Icon(Icons.refresh, size: 18),
                label: const Text('同步订阅'),
              ),
            ],
          ),
          const SizedBox(height: 8),
          const Text(
            '每份订阅独立计量，购买与续订在浏览器中完成。',
            style: TextStyle(color: muted, fontSize: 12),
          ),
        ],
      ],
    );
  }
}
