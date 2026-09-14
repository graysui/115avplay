# MediaVault 虚拟媒体服务器 — 实施计划 (plan.md)

> **文档定位**：本文档是实施**总览与拆分索引**。具体可执行任务已拆分至 [`phases/`](./phases/) 目录，每个阶段一个文件。
> 需求见 [`spec.md`](./spec.md)；技术细节以 [`../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md`](../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md) 为权威。

---

## 1. 里程碑总览

| 阶段 | 文件 | 目标 | 关键交付物 | 依赖 | 预估 |
|---|---|---|---|---|---|
| **P0** | [00-foundation.md](./phases/00-foundation.md) | 工程骨架与基础设施 | Go module、目录结构、配置、日志、SQLite 连接、迁移框架 | — | 1~2 天 |
| **P1** | [01-data-layer.md](./phases/01-data-layer.md) | 数据层与模型 | 9 张表 DDL/迁移、models、queries、仓库层 | P0 | 2~3 天 |
| **P2** | [02-115-client.md](./phases/02-115-client.md) | 115 OpenAPI 客户端 | OAuth、目录树、离线转存、删除、取直链 | P1 | 3~4 天 |
| **P3** | [03-ingestion.md](./phases/03-ingestion.md) | 离线数据摄取 | 30D 下载、AES-GCM 解密、合并去重、分类过滤 | P1 | 2~3 天 |
| **P4** | [04-scraping.md](./phases/04-scraping.md) | JavDB 刮削与优选 | API 客户端、限流、状态机、评分、榜单、落库推导 | P3 | 4~5 天 |
| **P5** | [05-playback-115.md](./phases/05-playback-115.md) | 资源调度与播放 | 可播性重算、Tier 调度、转存 Worker、Janitor、302 + Fallback | P2, P4 | 4~5 天 |
| **P6** | [06-emby-protocol.md](./phases/06-emby-protocol.md) | Emby 协议模拟 | 握手/鉴权/库/搜索/详情/图片/流/进度 | P5 | 4~5 天 |
| **P7** | [07-admin-console.md](./phases/07-admin-console.md) | 管理后台 | REST API、WS 日志、Vue 前端、embed | P5, P4 | 4~6 天 |
| **P8** | [08-hardening-release.md](./phases/08-hardening-release.md) | 可观测性与发布 | 告警、健康检查、测试、单二进制打包 | P6, P7 | 3~4 天 |

> 预估为单人工时粗估，仅用于排期参考。

## 2. 拆分索引

```
specs/mediavault-server/
├── spec.md                     # 需求规格
├── plan.md                     # 本文件（总览 + 拆分索引）
└── phases/
    ├── 00-foundation.md        # P0 工程骨架与基础设施
    ├── 01-data-layer.md        # P1 数据层与模型
    ├── 02-115-client.md        # P2 115 OpenAPI 客户端
    ├── 03-ingestion.md         # P3 离线数据摄取
    ├── 04-scraping.md          # P4 JavDB 刮削与优选
    ├── 05-playback-115.md      # P5 资源调度与播放
    ├── 06-emby-protocol.md     # P6 Emby 协议模拟
    ├── 07-admin-console.md     # P7 管理后台
    └── 08-hardening-release.md # P8 可观测性与发布
```

## 3. 关键路径与并行

```
P0 ── P1 ── P2 ──────────────┐
       │                     ├── P5 ── P6 ──┐
       └── P3 ── P4 ─────────┘              ├── P8
                          └── P7 ───────────┘
```

- **关键路径**：P0 → P1 → P2 → P5 → P6 → P8
- **可并行**：P3/P4（摄取与刮削）可与 P2（115 客户端）并行；P7（前端）可在 P5 接口稳定后启动
- **尽早验证的高风险点**：
  1. P2 的 115 取直链与转存（唯一未验证的外部链路）
  2. P5 的播放 fallback 与可播性一致性
  3. P4 的 JavDB 限流与封禁规避

## 4. 风险与缓解

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| R1 | 115 OpenAPI 变更 / 风控 | 播放与转存不可用 | 客户端抽象 + 降级；可观测告警；多 app_id 轮换 |
| R2 | JavDB 接口变更 / 封禁 | 刮削与在线搜索失效 | 令牌桶 + 随机延时 + 代理；失败熔断；本地库兜底 |
| R3 | AVDB 数据包格式变更（加密/字段） | 增量同步失败 | 解密器与解析器隔离；失败告警不阻断其他功能 |
| R4 | 大量临时转存占满 115 空间 | 空间不足 | Janitor TTL + 并发上限 + 空间告警 |
| R5 | 客户端（Infuse/VidHub）兼容差异 | 无法接入 | 以 `emby_protocol_endpoints.md` 最小集为准，逐客户端实测 |
| R6 | 部分成功的元数据长期不齐 | 详情页质量差 | `is_enriched=3` 持续重试；监控 3 的占比 |

## 5. 完成定义 (Definition of Done)

一个阶段视为完成，需同时满足：
1. 该阶段 `phases/*.md` 中所有任务勾选完成；
2. 阶段验收标准（Acceptance）全部通过；
3. `go build ./...` 与 `go vet ./...` 通过；
4. 关键路径有单元测试（覆盖率非硬指标，但核心算法必须有）；
5. 阶段涉及的 `spec.md` 验收场景（AC）可在本地手动复现；
6. 文档同步：如实现偏离设计，回写 PROJECT_SPEC 对应章节。

## 6. 约定

- **任务 ID**：`T-<阶段号><序号>`，如 `T-203` 表示 P2 第 3 个任务；
- **优先级**：`P0` 阻断后续 / `P1` 阶段内必须 / `P2` 可延后；
- **状态标记**：`[ ]` 未开始、`[~]` 进行中、`[x]` 完成、`[!]` 阻塞。
