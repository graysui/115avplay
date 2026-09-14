# MediaVault 虚拟媒体服务器 — 功能规格 (spec.md)

> **文档定位**：本文档定义"做什么 / 为什么做 / 验收标准"（需求层）。
> 详细技术设计（Schema、算法、接口字段）以 [`../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md`](../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md) 为权威。
> 实施步骤见 [`plan.md`](./plan.md) 与 [`phases/`](./phases/)。

---

## 1. 概述

构建一个**纯数据库驱动的 Go 单二进制虚拟媒体服务器**，以本地 SQLite 离线库（21 万番号 / 32 万磁力）为唯一真实源，通过模拟 Emby 协议向 Infuse / VidHub / Kodi 等客户端提供媒体库浏览与 **115 网盘 302 秒播**，并具备 JavDB 在线刮削、磁力优选、两级 115 资源调度与临时文件自动清理能力。

## 2. 目标 / 非目标

### 2.1 目标
1. 零本地媒体文件、零 STRM 文件，媒体库完全由数据库投影生成；
2. 对标准 Emby 客户端"无感接入"；
3. 播放走 115 CDN 302 直连，起播 < 1s（资源已就绪时）；
4. 自动刮削元数据、自动优选磁力、自动清理临时文件，长期无人值守运行。

### 2.2 非目标（明确不做）
- 不做 HLS/FFmpeg 实时转码（仅 DirectPlay / DirectStream）；
- 不做蓝光原盘/VOB 直读、声纹片头识别、弹弹play 弹幕（属原插件能力，见 `MEDIA_LIBRARY_SPEC.md` Part 1，本轮不移植）；
- 不做多租户权限体系（单机部署，默认单 admin + 可选多用户）；
- 不做拼音首字母搜索。

## 3. 术语

| 术语 | 含义 |
|---|---|
| 番号 / code | 规范化影片唯一标识，如 `SSIS-123`、`FC2-PPV-1234567` |
| 磁力 / magnet | `offline_magnets` 中的一条可播放资源记录 |
| Tier 1 | 115 已有永久库中的资源（`source_type='existing'`） |
| Tier 2 | 由磁力临时转存到 115 的资源（`source_type='temporary'`） |
| 优选 / preferred | 某番号下 `is_preferred=1` 的默认磁力 |
| 可播 | `target_pick_code` 非空且资源未过期（见 PROJECT_SPEC §8.0） |

## 4. 用户故事

| ID | 角色 | 故事 | 优先级 |
|---|---|---|---|
| US-1 | 观影用户 | 在 Infuse 中按分类浏览海报墙，快速找到想看的影片 | P0 |
| US-2 | 观影用户 | 搜索番号，本地未收录时系统自动联网补全并返回结果 | P0 |
| US-3 | 观影用户 | 点击播放，已就绪资源 302 秒开；未就绪时看到"准备中"而非报错 | P0 |
| US-4 | 观影用户 | 查看详情页多个版本（中字/破解/4K），并自由切换 | P1 |
| US-5 | 观影用户 | 断点续播、收藏、标记已看，多设备共享进度 | P1 |
| US-6 | 管理员 | 一键触发 30 天增量同步与 JavDB 榜单抓取 | P0 |
| US-7 | 管理员 | 指定日期范围批量刮削元数据，并实时查看进度与日志 | P1 |
| US-8 | 管理员 | 配置 115 临时目录、清理 TTL、并发与代理 | P1 |
| US-9 | 管理员 | 收到 115 token 失效 / JavDB 风控等告警 | P2 |
| US-10 | 运维 | 服务长期无人值守，临时文件自动清理、失效资源自动回退 | P0 |

## 5. 功能需求 (FR)

### 5.1 数据与持久化 (FR-DATA)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-DATA-1 | 系统以 SQLite（WAL）为唯一真实源，含 9 张表（见 PROJECT_SPEC §4） | 启动即建表，幂等 |
| FR-DATA-2 | 支持打开既有 `offline_full_merged.db` 并自动补列/补表迁移 | 不丢数据、可重复执行 |
| FR-DATA-3 | 每个番号至多一条 `is_preferred=1` | 部分唯一索引强制 |
| FR-DATA-4 | 磁力以 `info_hash` 全局去重，支持 `btih`/`ed2k`/`existing` 三类资源 | 见 §5.4 |

### 5.2 数据摄取 (FR-INGEST)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-INGEST-1 | 每日增量拉取 AVDB-Only 30D 包并解密入库 | AES-256-GCM 解密成功 |
| FR-INGEST-2 | 过滤 VR/欧美/写真；FC2/国产标记免刮削 | 过滤后分类分布正确 |
| FR-INGEST-3 | 跨源按 `info_hash` 去重，重复自动忽略 | 重复率可观测 |
| FR-INGEST-4 | 支持手动触发与定时（04:00）触发 | 一次触发一个任务，不并发重叠 |

### 5.3 刮削与优选 (FR-SCRAPE)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-SCRAPE-1 | 对 `is_enriched IN (0,3)` 的影片抓取 JavDB 元数据 | 成功回填标题/简介/封面/演员/标签 |
| FR-SCRAPE-2 | 请求成功但字段不齐时置 `is_enriched=3`，不冻结 | 下一批次仍会处理 |
| FR-SCRAPE-3 | 失败记录原因与重试次数；超 10 天或重试≥3 次熔断为 `2` | 不再无意义重试 |
| FR-SCRAPE-4 | 按"中字>破解>4K>有码>体积>唯一"计算 `priority_score` 并重选 preferred | 分数与首选可复算一致 |
| FR-SCRAPE-5 | 支持日/周/月榜与 TOP250 抓取并补全本地未收录番号 | 榜单番号入库 |
| FR-SCRAPE-6 | 限流：令牌桶 + 随机延时 + 可配代理，避免封禁 | 无 429/403 雪崩 |

### 5.4 资源调度与播放 (FR-STREAM)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-STREAM-1 | Tier1 优先：定时扫描 115 已有目录，写入 `existing` 资源 | 命中即 302 秒播 |
| FR-STREAM-2 | Tier2 兜底：对 preferred 磁力发起 115 离线转存 | 就绪后可播 |
| FR-STREAM-3 | 播放取链失败自动置不可播并回退（不返回死链 302） | 无破碎跳转 |
| FR-STREAM-4 | 转存中就绪等待 ≤ `stream_wait_ms`，超时返回"准备中"占位流 | 客户端可重试成功 |
| FR-STREAM-5 | Janitor 按 TTL 自动物理清理临时文件，仅限白名单目录 | 永不删已有影视目录 |
| FR-STREAM-6 | 同一番号并发转存 single-flight 合并 | 不重复占用 115 配额 |

### 5.5 Emby 协议 (FR-EMBY)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-EMBY-1 | 支持握手、认证、用户信息接口 | Infuse/VidHub 可登录 |
| FR-EMBY-2 | 支持媒体库分类、条目列表、分页、排序、搜索 | 海报墙正常 |
| FR-EMBY-3 | 详情页下发 `MediaSources` 多版本与体积 | 可切换版本 |
| FR-EMBY-4 | 图片接口提供 Primary/Backdrop（代理缓存 + 占位图） | 海报不白屏 |
| FR-EMBY-5 | 播放接口 302 重定向到 115 CDN | 可播放 |
| FR-EMBY-6 | 进度/收藏/已看上报与"继续观看" | 断点续播跨端一致 |
| FR-EMBY-7 | 搜索本地未命中时联网补全；失败返回空列表而非 5xx | 客户端不弹错 |

### 5.6 管理后台 (FR-ADMIN)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-ADMIN-1 | 仪表盘统计 `is_enriched` 0/1/2/3 分布 | 数字与库一致 |
| FR-ADMIN-2 | 番号/磁力浏览、检索与手动修正 | 可改可查 |
| FR-ADMIN-3 | 全量刮削（日期范围 + 含失败）与进度展示 | 参数生效 |
| FR-ADMIN-4 | 榜单抓取、30D 增量手动触发 | 按钮可用 |
| FR-ADMIN-5 | 实时日志 WebSocket 推送 | 日志实时可见 |
| FR-ADMIN-6 | 系统设置读写（115 目录、TTL、并发、代理等） | 脱敏返回 secret |
| FR-ADMIN-7 | 管理员登录鉴权 | 未登录拒绝 |

### 5.7 可观测性与运维 (FR-OPS)
| ID | 需求 | 验收要点 |
|---|---|---|
| FR-OPS-1 | `/healthz` 存活、`/readyz` 就绪（DB/115 token/同步时间） | 探针可用 |
| FR-OPS-2 | 告警：115 token 失效、JavDB 风控、转存失败、磁盘不足 | 支持 Webhook + 去重静默 |
| FR-OPS-3 | 日志级别与保留天数可配 | 可清理 |

## 6. 非功能需求 (NFR)

| ID | 需求 |
|---|---|
| NFR-1 | 单二进制交付，`go:embed` 打包前端，镜像 < 50MB |
| NFR-2 | 常驻内存 20~40MB（空闲），10 万级影片列表查询 < 100ms |
| NFR-3 | 播放就绪资源 302 响应 < 100ms（不含 115 网络） |
| NFR-4 | 刮削默认低并发（2 协程）+ 随机延时，避免触发风控 |
| NFR-5 | 所有外部调用可配超时、可配代理；失败不导致进程崩溃 |
| NFR-6 | 数据库迁移幂等、只增不删、可回滚重启 |
| NFR-7 | 敏感配置（115 Cookie/Token）加密存储 |

## 7. 数据契约摘要

9 张表（详见 PROJECT_SPEC §4）：
`offline_movies`、`offline_magnets`、`users`、`auth_sessions`、`user_progress`、`play_sessions`、`libraries`、`system_settings`、`schema_meta`。

关键状态枚举：
- `offline_movies.is_enriched`：`0` 待刮削 / `1` 完整成功 / `2` 免刮削或放弃 / `3` 部分成功
- `offline_magnets.resource_kind`：`btih` / `ed2k` / `existing`
- `offline_magnets.transfer_status`：`0` 未转存 / `1` 转存中 / `2` 就绪 / `-1` 失败 / `3` 已清理

## 8. 对外接口摘要

- **Emby 协议**：`/emby/system/*`、`/emby/users/*`、`/emby/items/*`、`/emby/library/mediafolders`、`/emby/videos/{id}/stream`、`/emby/sessions/playing*`
- **管理 API**：`/api/admin/*`、`/api/config`、`/api/movies*`、`/api/scraper/*`、`/api/rankings/*`、`/api/sync30d/*`、`/api/tasks/*`、`/api/logs/stream`
- **探针**：`/healthz`、`/readyz`

## 9. 验收标准（端到端）

| # | 场景 | 期望 |
|---|---|---|
| AC-1 | 首次启动空库 | 自动建表、创建 admin、`/readyz` 通过 |
| AC-2 | 导入 30D 增量包 | 新番号入库、重复磁力被忽略、分类过滤生效 |
| AC-3 | 刮削 100 部新片 | 完整成功置 1、字段不齐置 3、超期置 2，且 3 会在下批被重试 |
| AC-4 | 榜单抓取发现未收录番号 | 自动搜索、落库、优选 preferred，客户端可搜到 |
| AC-5 | 播放 Tier1 已就绪资源 | 302 到 115 CDN，客户端秒开 |
| AC-6 | 播放 Tier2 未转存资源 | 触发转存，等待内就绪则 302；超时返回占位流且重试成功 |
| AC-7 | 播放已失效资源 | 自动置不可播并回退，不返回死链 |
| AC-8 | TTL 到期 | Janitor 删除临时文件，库中资源置不可播，白名单外零删除 |
| AC-9 | 多用户进度 | A 用户进度不影响 B；两端可续播 |
| AC-10 | 115 token 失效 | 触发告警且不影响其他功能 |

## 10. 参考文档

| 文档 | 用途 |
|---|---|
| [`docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md`](../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md) | 权威技术设计 |
| [`docs/MEDIA_LIBRARY_SPEC.md`](../../docs/MEDIA_LIBRARY_SPEC.md) | 原插件机制参考 |
| [`references/115_openapi_field_spec.md`](../../references/115_openapi_field_spec.md) | 115 字段与坑点 |
| [`references/115_tree_scan_and_cleanup_spec.md`](../../references/115_tree_scan_and_cleanup_spec.md) | 目录扫描/番号正则/清理 |
| [`references/emby_protocol_endpoints.md`](../../references/emby_protocol_endpoints.md) | Emby 最小接口清单 |
| [`plan.md`](./plan.md) | 实施总览与拆分索引 |
