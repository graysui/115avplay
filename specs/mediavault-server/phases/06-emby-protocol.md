# P6 · Emby协议与播放进度

[总计划](../plan.md) · [需求](../spec.md) · [追踪](../traceability.md)

## 范围与依赖

前置：P5；G0客户端EC-1/2及媒体字段契约已验证。

对应设计：主设计§9；references/emby_protocol_endpoints.md。对应需求：FR-EMBY-1～7、FR-ADMIN-7。本文件定义待实施任务，不是通过记录。

## 任务清单

| ID | 状态 | 优先级 | 任务 |
|---|---|---|---|
| T-601 | [x] | 必须 | 路由前缀/静态段大小写别名、稳定ServerId/ItemId/SourceId、system握手 |
| T-602 | [x] | 必须 | authenticatebyname与audience=emby票据，hash存储与到期撤销 |
| T-603 | [x] | 必须 | 所有token格式与冲突处理；用户归属/enabled/到期校验；普通票据不可管理 |
| T-604 | [x] | 必须 | users/me、本人User DTO、views、session capabilities与logout |
| T-605 | [x] | 必须 | mediafolders映射可重叠六视图，库ID稳定、影片去重 |
| T-606 | [x] | 必须 | 列表ParentId/类型/递归/SortBy/SortOrder/分页/搜索、总数与参数上限 |
| T-607 | [x] | 必须 | 精确番号调用P4搜索服务，服务未连接时返回本地结果并明确阶段桩；P8接完整实现 |
| T-608 | [x] | 必须 | 详情和PlaybackInfo：真实MediaSources/媒体能力、PlaySessionId、时长评分映射 |
| T-609 | [x] | 必须 | 图片授权、ETag/标签、代理缓存/预算与失败占位，不代理任意客户端URL |
| T-610 | [x] | 必须 | GET/HEAD stream接Resolver，HEAD无新任务副作用；Range/显式版本/URL基址 |
| T-611 | [x] | 必须 | playing/progress/stopped幂等、活动会话写权限、lease及未知时长；拒绝关闭会话迟到事件 |
| T-612 | [x] | 必须 | 收藏/手动已看/resume/latest/counts；series nextup返回空集合 |
| T-613 | [x] | 必须 | Infuse/VidHub固定版本实机：登录/浏览/协商/选版/播放/冷准备/续播 |

## 验收与交付

1. 两客户端正式服务全链路通过；对扩展客户端不凭推断标兼容。
2. 媒体能力声明为DirectPlay，不宣称转码/重封装；未知字段不虚构。
3. 准备不更新用户进度，重复stopped不重复计数，用户及设备会话隔离正确。
4. 本地可先以P4接口桩验收协议结构；P8必须使用真实P4能力完成AC-4/7。

交付实现、对应契约/故障样例、测试结果及阶段状态。构建和测试要求见总计划；涉及后续阶段的端到端行为由P8统一验收，不以早期接口桩替代正式结果。
