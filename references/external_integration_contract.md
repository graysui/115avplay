# 外部集成证据与实现前置合同

状态：**待实测**。现有 115 字段记录、JavDB Python 工具和旧 Emby 提取文本仅证明历史行为或提供线索。本文件定义要验证的边界和新服务内部适配接口，不声称下面的候选 URL 在当前账号/应用下已可用。

## 1. 证据记录格式

每个能力保存：provider、应用/授权方式、测试日期、客户端/设备版本、请求方法与路径、脱敏 headers/body、原始响应、归一化结果、前置条件、成功/失败结果、响应语义来源。原始响应与包装后的字段分开；去掉 token/cookie/签名直链和个人文件信息。没有真实证据不能填写 passed。

G0 的记录放 references/fixtures/<provider>/，仅提交脱敏 fixture 和结果元数据；真实凭据保存在本机密钥配置。涉及云文件的验证仅使用明确属于该验证的独占临时目录，验证报告记录创建与清理结果。

## 2. 115 内部适配接口

这些是 Go 层目标 DTO，不是 115 原始字段名。

| 操作 | 输入 | 必需归一化输出/错误 |
|---|---|---|
| StartAuthorization | app_id、受支持授权参数 | authorization_id、交互信息、expires_at |
| PollAuthorization | authorization_id | pending/confirmed/expired、稳定 provider_user_id、令牌有效期 |
| Refresh | 加密 refresh_token、当前 revision | 新 access/refresh token 与 expires_at；轮换结果原子持久化 |
| ListFiles | binding_id、root_id、offset/limit、类型 | 文件 ID、父 ID、类别、大小字节、pick_code、时间、分页完成条件 |
| AddTransfer | binding、resource_kind、规范链接、独占目标目录 | remote_task_id 或可重查的资源身份；已存在/已接收/未知不能混为成功 |
| GetTransfer | remote_task_id/可验证关联键 | queued/running/ready/failed、进度、结果目录/文件集合 |
| GetDownloadURL | file_id/pick_code、客户端上下文 | URL、有效期（可 unknown）、必要 headers/UA/IP 条件 |
| DeleteOwnedAsset | 文件/目录清单及归属证据 | accepted/already_missing；不能直接当 completed |
| ConfirmDeletion | 同一批文件/目录 ID | confirmed_missing/present/unknown；回收站与空间状态分别报告 |

候选端点来自现有提取资料：passportapi.115.com/open/authDeviceCode、deviceCodeToToken、refreshToken；proapi.115.com/open/ufile/files、/open/offline/add_task_bt、/open/offline/add_task_urls、/open/ufile/downurl、/open/ufile/delete。**请求方法、字段、BT 前置解析、scope、额度、任务状态及各端点可用性均以 G0 新样例为准**；Cookie 的 lixian 参数不可直接拼到 OpenAPI 上。

P2 开始实现某个能力前，该能力必须已有 accepted fixture 或当前官方协议依据加验证记录；若请求契约仍未知，继续 G0 研究该能力，不能按函数名猜请求后将阶段标完成。

## 3. 必测矩阵

| ID | 验证 | 通过标准 |
|---|---|---|
| E115-1 | 初次授权、到期/轮换刷新、撤销 | 身份稳定；暂时失败不清除可恢复令牌；并发刷新只有一次有效提交 |
| E115-2 | 根/嵌套目录、分页、移动、已删除文件 | 字段归一化正确；not_found 与 auth/限流明确区分 |
| E115-3 | btih 添加、BT 前置条件、任务完成后文件定位 | 从所选版本得到真实主片，未知提交可对账，不重复盲发 |
| E115-4 | ed2k 添加与任务定位 | 独立通过；btih 通过不能替代 ed2k |
| E115-5 | CDN HEAD、GET Range 0-1、拖动 seek | 校验 206/Content-Range、实际字节；记录 UA/IP/headers/代理差异和有效期 |
| E115-6 | 删除提交、确认消失、回收站/空间变化 | 区分接受与完成；不扩大为整盘回收站清空 |
| E115-7 | 超时、429、凭据错误、额度不足 | 分类为 not_found/auth/rate_limited/transient/unsupported/quota，保留可恢复身份 |
| EJ-1 | 精确番号搜索、详情、磁力、评论 ed2k | 用真实字段证明中文/官方标题、简介、封面、演员、日期、评分等映射 |
| EJ-2 | 日/周/月/TOP250 与分页、过滤、429 | 分页不漏，拒绝类目不入库；限流能够停止/恢复 |
| EC-1 | Infuse、VidHub 各自登录到续播 | 固定版本和设备；记录完整请求链及显式多版本 |
| EC-2 | 两客户端冷准备超 8s、手动重试 | 可重试到所选真流，准备阶段不更新正片进度；记录通用错误文案限制 |

失败项保留失败 fixture 与限制。G0 未通过时可以做纯数据/迁移工作，但依赖该能力的发布验收保持未通过。不得通过简单改成“已验证”消除外部风险。

## 4. JavDB 来源映射

候选搜索为 /api/v2/search，详情/磁力/评论属于 /api/v1/movies/{id} 族，榜单为 /api/v1/rankings 与 /api/v1/movies/top。精确详情字段由 EJ-1 固定；搜索摘要不能代替完整详情。优先利用现有 Python 请求构造/签名作为参考，落入 Go 时必须保留请求/响应 fixture。

统一 DTO 包含 number、original_title、title_zh?、description_zh?、cover_url?、poster_url?、actors[]、tags[]、maker?、director?、rating_0_to_5?、release_date?、runtime_seconds?、video_type?。? 表示可空；上游不提供中文内容时不称为“官方中文翻译”，不复制日文到中文列。详情请求成功但缺字段按主设计 partial 处理。请求错误、解析结构变化和影片确实不存在为不同结果。
