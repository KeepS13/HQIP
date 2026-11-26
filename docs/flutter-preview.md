# HQIP Flutter Preview

独立预览产品，Windows 10 1809+/x64、Android 8+/ARM64 与 x64。沿用账户、订阅、节点、规则、计费和 VPN 核心。不会读取旧客户端状态，不迁移用户，不向旧客户端发布更新。

## 本地试用

运行 `dist/flutter-preview/HQIP-Preview-Windows-x64-Setup.exe` 安装。Windows 关闭窗口仅结束 Flutter 界面，连接与托盘继续运行；双击托盘或快捷方式重新打开，托盘菜单“退出”会断开并恢复网络设置。Android 安装对应架构 APK，连接前由系统请求 VPN 授权。

身份隔离：应用名 HQIP Preview；Android 包名 `online.sharevps.hqip.preview`；Windows 数据 `%LOCALAPPDATA%/HQIP/FlutterPreview`，安装 `%LOCALAPPDATA%/Programs/HQIP Preview`；Android 私有目录 `hqip-preview` 和 Keystore 别名 `hqip-preview-account-v1`。不同时连接两个 VPN 客户端。

## 架构

- `flutter-client`：Flutter 3.50.1、Material 3、Riverpod。连接、订阅、更新、设置四个页面。快速选择合并为最新意图，单调快照序号阻止旧响应覆盖新状态。隐藏页面暂停事件订阅，不自动发起延迟探测。
- `cmd/hqip-flutter-host`：Go 控制器及 Win32 托盘。产物名为 `hqip-runtime.exe`，沿用既有 TUN 进程排除规则。Named Pipe 仅授权当前用户 SID；界面以独立 `--attach` 子进程桥接，不开放本地 TCP 端口。窗口离开不销毁控制器。
- `native/mobile` + `flutter-client/android`：Platform Channels、Go mobile、Android Keystore 与 VpnService。账户令牌和节点凭据不进入 Dart；界面事件每秒最多一次。
- `internal/flutterrelease`：独立 schema 2、product `hqip-flutter`、channel `preview`，Ed25519 签名、SHA-256、版本序号防回退、大小上限。Windows ZIP 内逐文件清单绑定在签名中。
- `cmd/hqip-flutter-launcher`：稳定启动器，完整版本目录先校验后切换，原子更新 active/pending 指针；启动失败回退上一版本或安装内置版本。

## 更新语义

这里的自动更新是自动检查与下载安装包，**不是替换正在运行的 Dart 代码**。首次启动和每六小时检查，Windows 自动下载；Android 非计费网络自动下载，计费网络可手动下载。下载复用已有断点续传传输层。下载安装前校验签名与内容。

用户确认后 Windows 断开连接、退出界面和核心、重启激活新版本；Android 调起系统安装页面，需要用户确认与“允许安装未知应用”权限。拒绝安装不会强制迁移。稳定启动器自身通过新的 EXE 安装包更新。

控制服务新增可选查询：`/api/v3/client/releases?client=flutter&channel=preview`、`/api/v1/user/installers?client=flutter&channel=preview`；只读取 `ArtifactDir/flutter-preview-releases.json`。文件位于 `/downloads/flutter/preview/{sha256}/{filename}`，支持验证后的 Range 下载。无查询参数的旧渠道保持原行为。

线上独立预览渠道由 Nginx 读取 `/opt/hqip-flutter-preview/current/` 中的签名目录，与控制服务的正式渠道存储隔离。官网不增加入口，正式客户端的更新和下载保持原行为。

## 重建

需要 Windows、Go 1.26.10、Python 3.12 + cryptography、JDK 21、MSVC C++/Windows SDK/CMake、Android SDK 36/Build Tools 36.0.0/NDK 30.0.16248370。Flutter 精确提交记录在 `packaging/flutter-version.json`，下载至 `.local/flutter-sdk`。

```powershell
python scripts/prepare_flutter.py tools --platform windows
python scripts/prepare_flutter.py tools --platform android
# 仅首次创建，已有身份会拒绝覆盖。确保 keytool 在 PATH。
python scripts/prepare_flutter.py signing
python scripts/build_flutter.py windows
python scripts/build_flutter.py android
```

设置 `JAVA_HOME`、`ANDROID_HOME` 与 JDK 的 PATH。Windows 产物包括完整离线安装 EXE、签名 ZIP；Android 为两个独立签名 APK。签名材料在忽略目录 `.local/flutter-signing/`，应私下备份并保持不变，后续更新必须使用同一 Ed25519 身份和 Android 证书，递增 sequence。不要使用正式客户端密钥。发布包不包含私钥。

`flutter-preview.yml` 的 PR 和 `test` 任务使用临时签名；仅主分支的手动 `publish` 任务读取独立预览 Secrets。正式发布要求当前主分支提交的完整 CI 通过。主分支变更后必须重新触发，发布和常规生产部署共享 `hqip-production` 并发锁。

## 检查

```powershell
go test -tags=with_utls ./internal/client ./internal/clientbridge ./internal/control ./internal/flutterrelease ./internal/previewhost ./cmd/hqip-flutter-host ./cmd/hqip-flutter-launcher
cd flutter-client
../.local/flutter-sdk/bin/flutter.bat analyze
../.local/flutter-sdk/bin/flutter.bat test
```

自动测试覆盖签名/内容损坏、跨渠道隔离、下载 Range、路径穿越、逐文件完整性、计费网络策略、旧快照与快速选择、隐藏订阅以及 320/360/412/900/1280/1440 宽度、100%/150%/200% 字号、安全区域和短窗口。设备上的真实账号、VPN 授权、断网恢复、计费、Android 安装确认仍需试用验收；构建通过不等于这些设备场景已经验收。


## 0.1.0-preview.2 界面重构

保留原有深色与紫色配色、绿色连接状态和 H 图标。公共主题统一卡片、按钮、输入框、字号和间距；桌面 208px 侧栏，移动端 56px 顶栏与紧凑底部导航。连接页在内容宽度 760px 以上采用双栏，订阅网格在 680px 以上采用双栏。登录面板只保留一处品牌，服务地址独立弹窗；预览说明移至设置的关于分组。

节点面板在手机上使用不超过可用高度 85% 的底部弹层，桌面使用最大 620px 弹窗。支持订阅分组、搜索、不可用状态与已有延迟结果。实际连接与目标节点同时比较订阅和节点标识。更新导航在安装包就绪时显示标记，安装、退出账户和完全退出仍要求确认。

`lib/ui/presentation.dart` 只订阅影响静态界面的字段，流量与节点检测各自更新；订阅有效期跨界时仍会刷新。原有 Session 排序、选择合并、窗口隐藏暂停订阅等逻辑保持不变。动画遵从系统减少动态效果设置。

默认 `flutter test` 为 47 项回归测试，另有一个按需开启的截图导出测试。图像导出使用离线示例数据，不进入产品入口，不访问真实账户；当前脚本从 Windows 系统加载微软雅黑用于中文截图：

```powershell
cd flutter-client
../.local/flutter-sdk/bin/flutter.bat test test/preview_capture_test.dart --dart-define=HQIP_CAPTURE_DIR=D:/workspace/HQIP-new/dist/flutter-preview/screenshots
```

本次已检查 Windows 本地发布构建以及 Android x64 雷电模拟器的登录、导航与设置界面。ARM64 APK 已构建和验签，但没有在 ARM64 真机上完成 VPN、真实登录和升级安装验收。最终产物、SHA-256、截图和验收记录分别位于 `dist/flutter-preview/`、`checksums.json`、`screenshots/README.md` 和 `TESTING.md`。preview.2 阶段没有发布线上更新渠道。


## 独立渠道发布与回退

首个线上版本为 `0.1.0-preview.3 / 3`。持续使用同一预览 Ed25519 公钥、Android 证书、应用包名和数据目录。Secrets 为 `HQIP_FLUTTER_UPDATE_SEED`、`HQIP_FLUTTER_ANDROID_KEYSTORE`、`HQIP_FLUTTER_ANDROID_SIGNING_JSON`，不得复用正式客户端签名；私钥不写入仓库、产物或日志。服务器登录使用既有 `DEPLOY_*`，严格验证主机密钥。

每次迭代同时提高 `packaging/flutter-version.json` 和 `flutter-client/pubspec.yaml` 的版本与序号。先执行 GitNexus 影响分析、测试与完整变更分析，提交到 main，等待该提交 CI 全绿，然后执行：

```powershell
gh workflow run flutter-preview.yml --repo KeepS13/HQIP --ref main -f action=publish
gh run list --repo KeepS13/HQIP --workflow flutter-preview.yml --limit 3
gh run watch <运行编号> --repo KeepS13/HQIP --exit-status
```

首次接入时先记录 `HQIP_DEPLOY_PAUSED` 原值，再设置为 `true`，待合入、CI 测试和预览发布完成后恢复原值。该变量只暂停常规生产部署，不跳过任何测试，避免本次重新发布旧客户端。后续预览发布不需要修改控制服务或数据库。

发布包含六个签名条目、四份文件（三个安装器和 Windows 更新 ZIP）、三个安装链接以及签名回执。回执绑定干净源提交、版本、序号、大小和 SHA-256。仅从该次构建传递完整签名包；Actions 存储不足时通过同次运行的未发布 draft 暂存，验收成功后移除。

服务器先验签和校验全部摘要及 Windows 逐文件清单，然后写入 `objects/<sha256>/<filename>` 和 `versions/<sequence>`，原子切换 `current` 并平滑重载 Nginx。同序号只允许相同回执重试；不同内容和旧序号会拒绝。历史对象保留供现有下载继续完成。`transactions/<run>-<attempt>/` 保存原配置及前一目录。

发布任务比较前后正式渠道 JSON、服务器正式目录全部文件摘要、控制服务 PID 和版本目录，验证两个网站及 `/readyz`、错误渠道 404、六个签名、四个完整下载摘要和 Range。任一验收失败自动恢复原 Nginx 配置及预览目录。验收记录打印在 CI 日志并保存在 `flutter-preview-acceptance` 产物中（存储配额允许时）。

手动回退使用成功发布的精确事务编号：

```powershell
gh workflow run flutter-preview.yml --repo KeepS13/HQIP --ref main -f action=rollback -f rollback_run=<运行编号>-<尝试编号>
```

回退拒绝覆盖后来修改的 Nginx 配置或预览目录；首次上线回退隐藏目录，后续回退恢复上一目录，均保留已上传文件。不强制降级已安装的新客户端，修复时使用更高序号。preview.2 自动发现、下载及“可安装”状态单独记录；安装由用户确认，ARM64 真机 VPN 验收不得用模拟器代替。
