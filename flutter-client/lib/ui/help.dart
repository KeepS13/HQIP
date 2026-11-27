import 'package:material_ui/material_ui.dart';

import 'design.dart';

Future<void> showUsageHelp(BuildContext context) => showDialog<void>(
  context: context,
  builder: (context) => AlertDialog(
    scrollable: true,
    title: const Text('使用帮助'),
    content: const SizedBox(
      width: 520,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          _HelpItem(
            '登录与连接',
            '从客户端点击“在浏览器中登录”，完成网页授权后返回客户端，再选择订阅和节点并点击“连接网络”。仅登录网站不会自动建立 VPN。',
          ),
          _HelpItem(
            '规则、全局与 TUN',
            '规则模式按访问目标分流，全局模式让普通流量通过节点。Windows 关闭 TUN 时使用系统代理，首次开启 TUN 需要系统授权；Android 使用系统 VPN。',
          ),
          _HelpItem(
            '订阅与节点切换',
            '每份订阅独立计量。同名节点通过订阅名称和编号区分。连接期间切换会先关闭旧连接再建立新连接；订阅到期或流量用尽后，不会自动改用另一份订阅。',
          ),
          _HelpItem(
            '两种延迟检测',
            '节点列表测试 QUIC/TLS 握手延迟，不会切换连接。流量区的“检测延迟”通过当前代理访问 HTTPS，会产生该订阅的用量。',
          ),
          _HelpItem(
            '同步与恢复',
            '账户或网络暂时不可用时保留登录状态。可在连接页刷新账户；已连接的节点会按已有授权尝试恢复，离线可用时间以客户端提示为准。',
          ),
          _HelpItem(
            '后台与退出',
            'Windows 关闭窗口后连接仍在后台运行；在设置或托盘中选择“退出并恢复网络”可完全退出。更新下载完成后，安装仍需你确认。',
          ),
        ],
      ),
    ),
    actions: [
      TextButton(
        onPressed: () => Navigator.pop(context),
        child: const Text('知道了'),
      ),
    ],
  ),
);

class _HelpItem extends StatelessWidget {
  const _HelpItem(this.title, this.body);
  final String title, body;
  @override
  Widget build(BuildContext context) => Padding(
    padding: const EdgeInsets.only(bottom: 16),
    child: Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(title, style: const TextStyle(fontWeight: FontWeight.w600)),
        const SizedBox(height: 6),
        Text(body, style: const TextStyle(color: muted, fontSize: 13)),
      ],
    ),
  );
}
