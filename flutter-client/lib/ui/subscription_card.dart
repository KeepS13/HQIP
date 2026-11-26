import 'package:material_ui/material_ui.dart';

import '../runtime/models.dart';
import 'design.dart';

String subscriptionStatus(Subscription item) {
  if (item.valid) return '可用';
  if (item.expires != null && !item.expires!.isAfter(DateTime.now())) {
    return '已到期';
  }
  if (item.quota != null && item.used >= item.quota!) return '流量已用完';
  return '不可用';
}

class SubscriptionCard extends StatelessWidget {
  const SubscriptionCard({
    super.key,
    required this.subscription,
    this.compact = false,
  });
  final Subscription subscription;
  final bool compact;
  @override
  Widget build(BuildContext context) {
    final item = subscription;
    return SectionCard(
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            compact ? '当前订阅' : subscriptionStatus(item),
            style: TextStyle(color: item.valid ? good : muted, fontSize: 12),
          ),
          const SizedBox(height: 6),
          Text(item.name, style: Theme.of(context).textTheme.titleMedium),
          const SizedBox(height: 20),
          Text(
            item.quota == null
                ? '不限总流量'
                : bytes((item.quota! - item.used).clamp(0, item.quota!)),
            style: const TextStyle(
              fontSize: 26,
              fontWeight: FontWeight.w600,
              height: 1.2,
            ),
          ),
          const SizedBox(height: 4),
          Text(
            item.quota == null
                ? '已用 ${bytes(item.used)}'
                : '剩余流量 · 已用 ${bytes(item.used)} / ${bytes(item.quota!)}',
            style: const TextStyle(color: muted, fontSize: 12),
          ),
          const SizedBox(height: 16),
          LinearProgressIndicator(
            value: item.fraction,
            minHeight: 4,
            borderRadius: BorderRadius.circular(4),
            color: item.valid ? accent : muted,
            backgroundColor: canvas,
          ),
          const SizedBox(height: 12),
          DetailRow('到期时间', date(item.expires)),
          if (!compact)
            DetailRow(
              '连接名额',
              '${number(item.raw['occupied']).toInt()} / ${number(item.raw['maxConnections']).toInt()}',
            ),
          if (compact && !item.valid)
            Text(
              subscriptionStatus(item),
              style: const TextStyle(color: muted, fontSize: 12),
            ),
          if (item.reserved > 0)
            Padding(
              padding: const EdgeInsets.only(top: 4),
              child: Text(
                '待结算预留 ${bytes(item.reserved)}',
                style: const TextStyle(color: muted, fontSize: 12),
              ),
            ),
        ],
      ),
    );
  }
}
