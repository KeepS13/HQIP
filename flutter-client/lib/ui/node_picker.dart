import 'dart:convert';

import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/models.dart';
import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';
import 'subscription_card.dart';

Future<void> showNodePicker(BuildContext context) {
  if (MediaQuery.sizeOf(context).width < 600) {
    return showModalBottomSheet<void>(
      context: context,
      isScrollControlled: true,
      useSafeArea: true,
      builder: (context) {
        final media = MediaQuery.of(context);
        return Padding(
          padding: EdgeInsets.only(bottom: media.viewInsets.bottom),
          child: SizedBox(
            height:
                (media.size.height -
                    media.viewInsets.bottom -
                    media.padding.top) *
                .85,
            child: const SafeArea(top: false, child: NodePicker()),
          ),
        );
      },
    );
  }
  return showDialog<void>(
    context: context,
    builder: (context) => Dialog(
      insetPadding: const EdgeInsets.all(24),
      child: SizedBox(
        width: 620,
        height: MediaQuery.sizeOf(context).height * .8,
        child: const NodePicker(),
      ),
    ),
  );
}

class NodePicker extends ConsumerStatefulWidget {
  const NodePicker({super.key});
  @override
  ConsumerState<NodePicker> createState() => _NodePickerState();
}

class _NodePickerState extends ConsumerState<NodePicker> {
  final search = TextEditingController();
  String query = '';
  @override
  void dispose() {
    search.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final app = ref.watch(uiStateProvider);
    final tests = jsonDecode(
      ref.watch(
        sessionProvider.select((s) => jsonEncode(s.snapshot.nodeTests)),
      ),
    ) as Map<String, dynamic>;
    final choices = app.snapshot.choices
        .where(
          (n) => ('${n.name} ${app.snapshot.package(n.subscriptionId)?.name}'
              .toLowerCase()
              .contains(query.trim().toLowerCase())),
        )
        .toList();
    final groups = <String, List<NodeChoice>>{};
    for (final node in choices) {
      groups.putIfAbsent(node.subscriptionId, () => []).add(node);
    }
    final results = objects(tests['results']);
    return Padding(
      padding: const EdgeInsets.all(16),
      child: Column(
        children: [
          Row(
            children: [
              Expanded(
                child: Text(
                  '选择节点',
                  style: Theme.of(context).textTheme.titleLarge,
                ),
              ),
              IconButton(
                tooltip: '关闭节点选择',
                onPressed: () => Navigator.pop(context),
                icon: const Icon(Icons.close, size: 20),
              ),
            ],
          ),
          const SizedBox(height: 8),
          TextField(
            key: const ValueKey('node-search'),
            controller: search,
            autofocus: MediaQuery.sizeOf(context).width >= 600,
            onChanged: (value) => setState(() => query = value),
            decoration: InputDecoration(
              prefixIcon: const Icon(Icons.search, size: 20),
              hintText: '搜索节点或订阅',
              suffixIcon: query.isEmpty
                  ? null
                  : IconButton(
                      tooltip: '清空搜索',
                      icon: const Icon(Icons.close, size: 18),
                      onPressed: () {
                        search.clear();
                        setState(() => query = '');
                      },
                    ),
            ),
          ),
          const SizedBox(height: 12),
          Expanded(
            child: ListView(
              padding: EdgeInsets.zero,
              keyboardDismissBehavior: ScrollViewKeyboardDismissBehavior.onDrag,
              children: [
                if (choices.isEmpty)
                  Padding(
                    padding: const EdgeInsets.symmetric(vertical: 24),
                    child: Text(
                      query.isEmpty ? '暂无节点，请先同步或添加订阅。' : '没有匹配的节点，试试其他关键词。',
                      style: const TextStyle(color: muted),
                    ),
                  ),
                for (final entry in groups.entries) ...[
                  Padding(
                    padding: const EdgeInsets.fromLTRB(4, 12, 4, 8),
                    child: Text(
                      '${app.snapshot.package(entry.key)?.name ?? '订阅'} · ${entry.value.length} 个节点',
                      style: const TextStyle(color: muted, fontSize: 12),
                    ),
                  ),
                  for (final node in entry.value)
                    Builder(
                      builder: (context) {
                        final selected = app.selected?.key == node.key;
                        final sub = app.snapshot.package(node.subscriptionId);
                        final available = node.ready && (sub?.valid ?? false);
                        num? latency;
                        for (final item in results) {
                          if (tests['subscriptionId'] == node.subscriptionId &&
                              item['nodeId'] == node.id &&
                              item['status'] == 'success') {
                            latency = item['delayMs'] as num?;
                          }
                        }
                        return Padding(
                          padding: const EdgeInsets.only(bottom: 6),
                          child: Semantics(
                            selected: selected,
                            enabled: available,
                            child: Material(
                              color: selected ? selectedSurface : canvas,
                              borderRadius: BorderRadius.circular(12),
                              child: InkWell(
                                key: ValueKey('node-${node.key}'),
                                borderRadius: BorderRadius.circular(12),
                                onTap: !available
                                    ? null
                                    : () {
                                        final session = ref.read(
                                          sessionProvider.notifier,
                                        );
                                        Navigator.pop(context);
                                        session.select(node);
                                      },
                                child: Padding(
                                  padding: const EdgeInsets.all(12),
                                  child: Row(
                                    crossAxisAlignment:
                                        CrossAxisAlignment.start,
                                    children: [
                                      Padding(
                                        padding: const EdgeInsets.only(top: 2),
                                        child: Icon(
                                          selected
                                              ? Icons.radio_button_checked
                                              : Icons.radio_button_off,
                                          size: 20,
                                          color: selected ? accent : muted,
                                        ),
                                      ),
                                      const SizedBox(width: 12),
                                      Expanded(
                                        child: Column(
                                          crossAxisAlignment:
                                              CrossAxisAlignment.start,
                                          children: [
                                            Text(
                                              node.name,
                                              style: TextStyle(
                                                color: available ? null : muted,
                                                fontWeight: selected
                                                    ? FontWeight.w600
                                                    : FontWeight.w400,
                                              ),
                                            ),
                                            const SizedBox(height: 4),
                                            Wrap(
                                              spacing: 8,
                                              runSpacing: 4,
                                              children: [
                                                if (selected)
                                                  const Text(
                                                    '已选择',
                                                    style: TextStyle(
                                                      color: accent,
                                                      fontSize: 12,
                                                    ),
                                                  ),
                                                Text(
                                                  !available
                                                      ? (sub != null &&
                                                                !sub.valid
                                                            ? subscriptionStatus(
                                                                sub,
                                                              )
                                                            : '暂不可用')
                                                      : latency == null
                                                      ? '尚未检测延迟'
                                                      : '${latency.toInt()} ms',
                                                  style: TextStyle(
                                                    color:
                                                        available &&
                                                            latency != null
                                                        ? good
                                                        : muted,
                                                    fontSize: 12,
                                                  ),
                                                ),
                                              ],
                                            ),
                                          ],
                                        ),
                                      ),
                                    ],
                                  ),
                                ),
                              ),
                            ),
                          ),
                        );
                      },
                    ),
                ],
                const Padding(
                  padding: EdgeInsets.only(top: 12, bottom: 8),
                  child: Text(
                    '连接期间选择新节点，将自动切换连接。',
                    style: TextStyle(color: muted, fontSize: 12),
                  ),
                ),
              ],
            ),
          ),
        ],
      ),
    );
  }
}
