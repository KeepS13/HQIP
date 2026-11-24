# 客户端安装与自动更新

归属：`client-presentation`，契约 `client-runtime.updates v1`。Windows 交付一个 `HQIP-Windows-x64-Setup.exe`；Android 手机交付 `HQIP-Android-arm64.apk`，x64 APK 用于相应设备和模拟器。

## 用户行为

Windows 安装包包含完整首版，无需手动解压或首次联网下载运行库。默认安装到 `%LOCALAPPDATA%/Programs/HQIP`，当前用户无需安装提权；桌面和开始菜单入口都指向固定启动器。TUN 的系统授权仍按原规则执行。已有便携版用户安装后沿用 `%LOCALAPPDATA%/HQIP/Godot` 的账户，先从旧客户端托盘正常退出；原 Wails 账户目录保持独立。

启动即在后台检查公开签名版本，不要求登录；后续每六小时检查，服务来源改变至多一分钟后检查。Windows 自动下载、验签并暂存，完全退出后下次打开切换，失败可回退。关闭窗口只隐藏至托盘，不能视为已经退出。Android 自动下载同架构 APK，用户点击安装并确认系统提示。普通应用无法无提示覆盖安装。

Windows 的更新传输仍是内部 ZIP，但用户只接触安装 EXE 和应用内更新。程序运行时需要界面、Go 运行时和代理内核等多个文件，所以“一个文件”指下载交付形式，安装后由软件维护完整目录。当前实现不是纯在线空壳，也不在代理连接期间替换进程。

覆盖安装保留 active/pending 指针，但启动器忽略低于安装包内置序号的指针，避免旧更新遮住新安装包；较新的已验证更新仍可使用。卸载删除安装目录中的程序和更新版本，保留独立账户数据；仅当开机启动项仍准确指向本次安装时才删除。安装/卸载通过启动器互斥量检查在运行的实例，要求正常退出，不强制结束网络进程。

## 构建

依赖原有 Go/Godot/Android 工具，Windows 新增 Inno Setup 6（本地验证 6.7.3）。`--iscc`、`HQIP_ISCC`、PATH、项目 `.local/godot-tools/inno/ISCC.exe` 或默认安装位置提供编译器。脚本不自动下载安装编译器；下载工具应验证官方发行签名。首次构建前沿用已有发布密钥，不重新生成身份。

```powershell
$clientRelease = Get-Content packaging/client-version.json -Raw | ConvertFrom-Json
python scripts/build_godot.py --platform all --version $clientRelease.version --sequence $clientRelease.sequence
python scripts/build_godot.py --platform android --android-arch amd64 --version $clientRelease.version --sequence $clientRelease.sequence
python -m unittest discover -s scripts -p test_package_godot_windows.py
go test -tags=with_utls ./internal/client ./internal/clientrelease ./cmd/hqip-launcher ./cmd/hqip-client-release
```

每次构建必须显式提供版本和序号，避免误用历史默认值。`godot-build.json` 的用户交付清单仅列安装 EXE/APK，不再列便携 ZIP。`HQIP-Godot-Windows-x64.zip` 仍是签名更新渠道的完整版本包。安装 EXE 在临时目录编译完成后原子替换，缺少任一必需文件即失败；编译失败不更新交付清单。

安装器中文语言文件来自 Inno Setup 官方仓库 `jrsoftware/issrc` 的 `is-6_7_3` 标签，`Files/Languages/Unofficial/ChineseSimplified.isl`，SHA-256 `7d544b9bb1d142cfa11f2e5d3cc8abe2e55f8e066c5124e3772675aa236e1278`；保留原文件作者信息。安装器使用既有 HQIP 图标。

## 上线边界

### 本地更新包保留

签名发布命令在清单原子写入成功后回收未引用的旧包，每个平台保留当前和上一版本。回收前验签所有保留记录并校验包大小、SHA-256；只处理 `godot/<sha256>/<约定包名>` 的单文件目录，不递归删除目录，不跟随链接，未知内容保持原样。清单损坏、保留包缺失或摘要异常时停止删除，输出错误；此时新清单可能已发布，修复文件后可幂等重试。发布与清理必须由同一个独占发布操作串行执行。

已有渠道可以只用公钥检查和清理，无需读取或初始化私钥：

```powershell
go run ./cmd/hqip-client-release -prune -public-key <现有公钥> -output dist/godot-channel
go run ./cmd/hqip-client-release -prune -apply -public-key <现有公钥> -output dist/godot-channel
```

第一条只预览，第二条执行。JSON 输出包含候选文件、摘要、字节数及实际删除数量，应保存在库外操作记录中。每次清理顺序读取保留包与候选包，内存用量有界；这项磁盘校验仅发生于本地发布/维护，不增加客户端轮询或业务数据库开销。

### 生产发布

本地打包不会部署生产服务。正式发布使用同一提交的 [CI 与部署流程](../deployment/client-release-ci.md)，包含控制端路由、签名清单、完整更新包和网站安装包入口。旧 5.x 预览版因版本编号重置需覆盖安装一次 1.0.0；之后按签名序号更新。大陆网络和 Android ARM64 真机仍需现场验证。

preview.16 本地验证阶段（2026-11-24）官方更新接口尚返回 404；这是早期记录，后续发布结果以 [CI 与部署记录](../deployment/client-release-ci.md) 为准。Windows EXE 尚未配置 Authenticode 发布者签名；应用内更新仍使用既有 Ed25519 身份，Android 沿用既有 release 签名。

## 本轮验证

- Go 完整目标包测试通过：`internal/client`、`internal/clientrelease`、`cmd/hqip-launcher`、`cmd/hqip-client-release`，Windows 原生入口编译通过。覆盖未登录启动下载、签名/摘要/尺寸/降级拒绝与启动回退。
- Python 五项打包边界测试通过：缺文件拒绝、编译失败保留旧产物、完整输出原子发布、路径空格、无效版本和显式编译器路径处理。
- 使用相同安装脚本、独立 AppId 和临时安装路径进行真实静默安装/修复/卸载：九个程序文件逐一校验 SHA-256；当前用户卸载注册正确；安装后的原生运行时通过隔离账户目录返回 preview.16；覆盖安装保留 active/pending；持有客户端互斥量时阻止安装和卸载；卸载清除程序版本与注册，保留安装目录外状态。
- Android arm64/x64 构建均通过 APK release 签名验证；Windows/Android 三个平台的实际发布清单均通过固定公钥 Ed25519 验签，并核对渠道包与交付文件的 SHA-256 和尺寸。此验证不代替 ARM64 真机、Windows 实际代理/TUN 或线上下载渠道验收。

GitNexus 修改前已重建索引并核对实际入口：Windows 构建与启动入口为 LOW；Android `NewRuntime` 为 UNKNOWN，已确认 `NativeHost.java` 的 JNI 调用。全工作区变更分析包含本轮开始前的服务端、界面等大量修改，不能把其整体风险或未跟踪文件遗漏当作本轮单独发布通过；本轮没有提交这些修改。索引工具出现增量关联异常与 FTS 构建警告，使用完整重建复核调用图。

操作记录在库外 `.local/godot-tools/installer-validation.json`、构建日志和 `dist/godot-build.json`；没有向业务数据库写入安装或验收记录。
