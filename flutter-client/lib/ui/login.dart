import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../runtime/session.dart';
import 'design.dart';
import 'presentation.dart';

class LoginPage extends ConsumerStatefulWidget {
  const LoginPage({super.key});
  @override
  ConsumerState<LoginPage> createState() => _LoginPageState();
}

class _LoginPageState extends ConsumerState<LoginPage> {
  String? server;

  Future<void> editServer() async {
    final value = await showDialog<String>(
      context: context,
      builder: (context) => _ServerDialog(
        initial: server ?? ref.read(sessionProvider).snapshot.server,
      ),
    );
    if (value != null && mounted) setState(() => server = value);
  }

  @override
  Widget build(BuildContext context) {
    final state = ref.watch(uiStateProvider);
    final waiting = state.snapshot.loginPending || state.busy.contains('login');
    return Center(
      child: ConstrainedBox(
        constraints: const BoxConstraints(maxWidth: 420),
        child: SectionCard(
          padding: 24,
          child: Column(
            mainAxisSize: MainAxisSize.min,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Align(
                alignment: Alignment.centerLeft,
                child: Container(
                  width: 48,
                  height: 48,
                  decoration: BoxDecoration(
                    color: selectedSurface,
                    borderRadius: BorderRadius.circular(12),
                  ),
                  child: const Icon(
                    Icons.lock_outline,
                    color: accent,
                    size: 24,
                  ),
                ),
              ),
              const SizedBox(height: 24),
              Text('登录 HQIP', style: Theme.of(context).textTheme.headlineSmall),
              const SizedBox(height: 8),
              const Text(
                '从下方按钮打开浏览器，登录并确认授权后自动返回。',
                style: TextStyle(color: muted, height: 1.7),
              ),
              const SizedBox(height: 24),
              FilledButton.icon(
                key: const ValueKey('login-primary'),
                onPressed: !state.ready || waiting
                    ? null
                    : () => ref.read(sessionProvider.notifier).command(
                        'login',
                        {'server': server ?? state.snapshot.server},
                      ),
                icon: Icon(
                  !state.ready || waiting
                      ? Icons.hourglass_top
                      : Icons.open_in_browser,
                  size: 18,
                ),
                label: Text(
                  !state.ready
                      ? '正在准备客户端…'
                      : waiting
                      ? '等待浏览器授权'
                      : '在浏览器中登录',
                  textAlign: TextAlign.center,
                ),
              ),
              if (waiting) ...[
                const SizedBox(height: 8),
                const Text(
                  '请在网页中点击“授权”。仅登录网站不会完成客户端授权。',
                  style: TextStyle(color: muted, fontSize: 12),
                ),
                TextButton(
                  onPressed: state.busy.contains('cancelLogin')
                      ? null
                      : () => ref
                            .read(sessionProvider.notifier)
                            .command('cancelLogin'),
                  child: const Text('取消登录'),
                ),
              ],
              const SizedBox(height: 8),
              TextButton.icon(
                onPressed: waiting ? null : editServer,
                icon: const Icon(Icons.dns_outlined, size: 16),
                label: const Text('服务地址'),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _ServerDialog extends StatefulWidget {
  const _ServerDialog({required this.initial});
  final String initial;
  @override
  State<_ServerDialog> createState() => _ServerDialogState();
}

class _ServerDialogState extends State<_ServerDialog> {
  late final controller = TextEditingController(text: widget.initial);
  String? error;
  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  void save() {
    final text = controller.text.trim();
    final uri = Uri.tryParse(text);
    if (uri == null ||
        uri.scheme != 'https' ||
        uri.host.isEmpty ||
        uri.userInfo.isNotEmpty) {
      setState(() => error = '请输入完整的 HTTPS 服务地址');
      return;
    }
    Navigator.pop(context, text);
  }

  @override
  Widget build(BuildContext context) => AlertDialog(
    scrollable: true,
    title: const Text('服务地址'),
    content: SizedBox(
      width: 360,
      child: Column(
        mainAxisSize: MainAxisSize.min,
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          const Text('使用账户所属服务的地址登录。', style: TextStyle(color: muted)),
          const SizedBox(height: 16),
          TextField(
            controller: controller,
            autofocus: true,
            keyboardType: TextInputType.url,
            autocorrect: false,
            textInputAction: TextInputAction.done,
            onSubmitted: (_) => save(),
            decoration: InputDecoration(
              labelText: 'HTTPS 服务地址',
              errorText: error,
              errorMaxLines: 3,
            ),
          ),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('取消'),
      ),
      FilledButton(onPressed: save, child: const Text('保存')),
    ],
  );
}
