# 套餐应用状态上线验收

源提交 `31d0e23504e8ed51b62ec919b8058d534ab0161d`，完整发布使用正式版本 `1.0.0 / 5000027`。范围为 [template-apply v3](../contracts/subscription-template-apply.md)：待应用按钮颜色与文字、固定版本的二次确认、浮动结果提示以及服务端应用版本持久化。本地验证见 [审查记录](../reviews/2026-11-28-template-apply-state.md)。

## CI 与重试

[运行 46048901428](https://github.com/KeepS13/HQIP/actions/runs/46048901428) 的12项测试/构建通过，第二次部署验收成功。UTC 2026-11-27 18:21:52开始，18:48:04最终完成，含重试26分12秒；下述本地时间为 Asia/Taipei 2026-11-28。

- 首轮02:35:28激活，Android x64 APK末尾32字节的公网 Range 请求在180秒后仍收到0字节，并返回HTTP/2 INTERNAL_ERROR。02:38:48开始自动回滚，02:39:01已回滚并保留实时数据。日志只能确认下载超时/流错误，未确定网络层根因。
- 回滚后独立确认活动代码仍为 `fa3b13e37e7013c7b46cba7b86c523fa50403632`、410用户／180订阅／28订单、两路readyz及18节点正常。持有部署锁后核实状态为 `rolled-back-data-preserved`、路径与manifest摘要匹配，将首轮代码完整移至首轮备份目录的 `failed-release`；未覆盖或删除文件。库外 `retry-archive-receipt.json` 记录操作。
- 只重跑失败部署作业，保留同一提交的12项门禁结果。第二轮02:44:07激活，02:47:26通过完整Windows安装包公网摘要验收，02:48:03部署作业成功；签名渠道与全部交付物的首尾范围检查仍完整执行。

成功备份：`/root/backups/hqip-ci-31d0e23504e8ed51b62ec919b8058d534ab0161d-46048901428-2`。

首轮备份及归档：`/root/backups/hqip-ci-31d0e23504e8ed51b62ec919b8058d534ab0161d-46048901428-1`。

## 最终独立核验

02:48:39之后核验：

- 活动源提交与目标一致，用户/订阅/订单仍为410/180/28，主密钥摘要与发布前一致。
- 两路本机readyz均 `ok=true`，18/18活动节点ready且核心健康。
- 用户站和管理站 `/login` 均HTTP200，用户站API `/healthz`、`/readyz` 均HTTP200且 `ok=true`。
- 管理站实际JS `index-BXBeMXt2.js` 与本地已通过浏览器回归的产物逐字节一致，SHA-256 `f4917db627f4ffaa8ebe0935d87450dedddd06681b1ccb057fbc40c7b601bc51`。
- CSS `index-ChZ0hh-N.css` 同样逐字节一致，SHA-256 `cc4dbdf74dce0e3620ab8742c619656fe35760b9939f26388ac2cba8d03d1133`。

首次本地浏览器核验等待 `networkidle` 超时；改为等待DOM就绪后逐项检查页面/资源/健康端点即通过，不将等待条件超时当作业务故障。生产核验没有为了测试而修改或应用任何真实套餐。旧版未保存应用版本的套餐可能显示待应用，管理员重新确认一次后建立准确状态。

库外证据：`.local/apply-state-ci-final.json`、`apply-state-deploy-failed.log`、`apply-state-deploy-final.log`、`apply-state-production-before.json`、`apply-state-production-rollback.json`、`apply-state-production-after.json`、`apply-state-production-public.json`、`apply-state-retry-archive.json`。保留用户原有23份独立未提交文件，逐份摘要相同。
