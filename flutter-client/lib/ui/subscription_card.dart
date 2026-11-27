import 'package:material_ui/material_ui.dart';

import '../runtime/models.dart';
import 'design.dart';

String subscriptionStatus(Subscription item) {
  final status = item.raw['status'];
  if (status == 'pending') return '待生效';
  if (status == 'disabled') return '已停用';
  if (status == 'removed' || status == 'revoked') return '已移除';
  if (status == 'expired') return '已到期';
  if (status == 'exhausted') return '流量已用完';
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
    this.selected = false,
    this.pending = false,
    this.onSelect,
  });
  final Subscription subscription;
  final bool compact;
  final bool selected, pending;
  final VoidCallback? onSelect;
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
          if (!compact) ...[
            const SizedBox(height: 6),
            Text(
              '#${item.id.length > 6 ? item.id.substring(item.id.length - 6) : item.id}',
              style: const TextStyle(color: muted, fontSize: 12),
            ),
          ],
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
          DetailRow(
            '共享带宽',
            item.raw['bandwidthMbps'] == null
                ? '不限速'
                : '${number(item.raw['bandwidthMbps'])} Mbps',
          ),
          if (!compact)
            DetailRow(
              '连接名额',
              '${number(item.raw['occupied']).toInt()} / ${number(item.raw['maxConnections']).toInt()}',
            ),
          if (!compact && item.raw['createdAt'] is String)
            DetailRow(
              '开通时间',
              date(
                DateTime.tryParse(item.raw['createdAt'] as String)?.toLocal(),
              ),
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
          if (!compact) ...[
            const SizedBox(height: 12),
            OutlinedButton.icon(
              key: ValueKey('subscription-${item.id}'),
              onPressed: item.valid && !selected && !pending ? onSelect : null,
              icon: Icon(
                selected ? Icons.check_circle_outline : Icons.swap_horiz,
                size: 18,
              ),
              label: Text(
                pending
                    ? '正在切换订阅…'
                    : selected
                    ? '本机当前选择'
                    : '在本机使用此订阅',
              ),
            ),
            const SizedBox(height: 8),
            const Text(
              '一次性额度，不按月重置。',
              style: TextStyle(color: muted, fontSize: 12),
            ),
          ],
        ],
      ),
    );
  }
}
