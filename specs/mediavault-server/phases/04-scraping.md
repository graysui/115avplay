# P4 · JavDB 刮削与优选引擎

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-SCRAPE
> 对应设计：`PROJECT_SPEC` §6 / §7.1–§7.4 / §5.2

## 目标
实现 JavDB 元数据刮削（含状态机与熔断）、磁力优选评分、榜首重选、榜单抓取与在线搜索落库。

## 前置依赖
P3（[`03-ingestion.md`](./03-ingestion.md)）提供待刮削数据（`is_enriched IN (0,3)`）。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-401 | JavDB 客户端：签名（`jdSignature`）、UA、代理、令牌桶限流 + 随机延时 | P0 |
| T-402 | 搜索接口 `GET /api/v2/search?q=&type=movie&limit=5`，含精确番号匹配 | P0 |
| T-403 | 磁力/评论提取：`/api/v1/movies/{id}/magnets` + `/reviews`（评论区 ed2k 清洗与去重） | P0 |
| T-404 | 刮削 Worker 状态机：成功判定关键字段完整性 → `1`/`3`；失败记原因与重试；10 天/重试≥3 熔断 → `2` | P0 |
| T-405 | 元数据回填：`title_zh`/`description_zh`/`cover_url`/`poster_url`/`actors`/`tags`/`maker`/`director`/`score` | P0 |
| T-406 | 优选评分 `CalculatePriorityScore(title,sizeMB,hasOnlyOne)`，写回 `priority_score` | P0 |
| T-407 | 首选重选：事务内清旧置新，规则 `可播优先 → score DESC → size DESC → info_hash ASC` | P0 |
| T-408 | 榜单抓取：`/api/v1/rankings`（日/周/月）、`/api/v1/movies/top`（TOP250 分页） | P1 |
| T-409 | 榜单对账补全：本地未收录 → 搜索 → 落库（字段推导见 PROJECT_SPEC §7.4） | P1 |
| T-410 | 在线搜索自愈：本地未命中触发、single-flight、超时、失败降级 | P1 |
| T-411 | 调度控制：日期范围全量刮削、包含已失败、并发/延时/代理可配、任务进度 | P1 |
| T-412 | 单测：评分算法、状态机迁移、熔断边界、ed2k 键、preferred 唯一性 | P0 |

## 交付物
- `internal/javdb/`（`client.go`/`scraper.go`/`rankings.go`/`search.go`/`priority.go`/`ingest.go`/`ratelimit.go`）
- 可对指定日期范围批量刮削并输出进度与熔断统计

## 验收标准
1. 刮削 100 部新片：字段齐全置 `1`，缺封面/简介置 `3`，且 `3` 会在下一批被再次处理；
2. 发布超 10 天或重试 ≥3 次仍失败的资源被置 `2` 且不再重试；
3. `priority_score` 与 `is_preferred` 可按规则复算一致，同番号仅一条 preferred；
4. 抓取周榜后，本地未收录番号被搜索、落库，客户端可检索到；
5. 评论区 ed2k 资源以 `resource_kind='ed2k'` 正确入库；
6. 触发风控时按退避策略重试，不产生 429/403 雪崩。

## 风险 / 备注
- JavDB 为逆向接口，可能随时变更；客户端与解析需隔离，便于单点替换。
- 评分输入口径**仅读 `offline_magnets.title`**，`quality_label` 不参与评分。
- 榜单/在线搜索落库需 `ON CONFLICT DO UPDATE` 且不覆盖已有离线元数据。
