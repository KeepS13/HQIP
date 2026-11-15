# Windows 原生图标构建

`python scripts/build_desktop.py` 使用本目录固定的 `winres v0.3.1` 与 `go.sum`，将 `desktop/assets/hqip.ico` 的全部尺寸写入 Windows 图标资源组 **ID 3**。Wails v3.1.0 使用此 ID 显示窗口与任务栏图标；Explorer 也直接读取 PE 资源。

`desktop/main.go` 单独嵌入同源 PNG，供 Wails 应用、托盘及无 PE 资源的开发构建使用。只设置运行时图标不会改变 Explorer 图标，所以发布必须运行打包脚本。

默认图标和五种 VPN 状态图标由 `scripts/build_desktop_icons.py` 统一生成 SVG、PNG 和多尺寸 ICO（需要 Pillow）。修改设计后运行该脚本并提交生成资产；普通构建不需要 Pillow。运行时托盘使用状态 ICO，窗口通过 `WM_SETICON` 同步小图标和大图标；更新与句柄释放均在 UI 线程进行。Wails 的 Windows `App.SetIcon` 是空实现，不能用于任务栏状态更新。

构建脚本生成临时 `.syso`，完成或失败后清理；不覆盖已有资源对象。打包前会读取产物 PE，逐项核对图标组及图片字节与源 ICO 一致，不需要启动客户端。依赖锁文件与确定的 ICO 使资源对象可以重复生成。

正在运行旧客户端时，使用独立输出目录：

```powershell
python scripts/build_desktop.py --output-dir dist/desktop-branded
```

ZIP 仍输出到 `dist/HQIP-Windows-x64.zip`。更新时先在托盘退出旧客户端，再解压新版到独立目录；构建和验证不会启动客户端或修改系统代理。
