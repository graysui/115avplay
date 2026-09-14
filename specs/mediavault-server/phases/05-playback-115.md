# P5 · 资源调度与播放（115 两级管线）

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-STREAM
> 对应设计：`PROJECT_SPEC` §8.0 / §8.1 / §8.2 / §8.3 / §8.4

## 目标
实现可播性权威判定、Tier1 已有库扫描、Tier2 临时转存、Janitor 清理，以及**播放 302 + 失败降级**闭环。

## 前置依赖
P2（[`02-115-client.md`](./02-115-client.md)）+ P4（[`04-scraping.md`](./04-scraping.md)）。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-501 | `recomputeAvailability(magnet)`：以 `transfer_status`+`pick_code` 为权威，派生 `is_available` | P0 |
| T-502 | Tier1 扫描 Worker：根 CID 遍历 → 目录名提号 → 最大视频 → upsert `existing` 资源（`priority_score=900000`） | P0 |
| T-503 | Tier2 转存 Worker：按 `resource_kind` 分发（btih→BT、ed2k→urls、existing 跳过）+ single-flight + 并发上限 | P0 |
| T-504 | Janitor：按 `cleanup_ttl_days` 清理，仅限 `temp_transfer_cid` 白名单之下，联动重算可播性 | P0 |
| T-505 | Stream Resolver：可播→`downurl`→302；失败置不可播；未就绪→等待/入队；超时→占位流 | P0 |
| T-506 | 占位流资产：`/static/preparing.mp4`（约 5s 循环）+ `X-MV-Preparing` 头 | P1 |
| T-507 | 播放会话与进度落库（供 P6 调用）：`play_sessions` / `user_progress` | P1 |
| T-508 | 集成测试：可播性一致性、fallback 全分支、清理不越界、并发 single-flight | P0 |

## 交付物
- `internal/services/`（`availability.go`/`tree_scanner.go`/`transfer_worker.go`/`janitor.go`）
- 一个可从 `pick_code` 直达 302 的 `Stream Resolver`

## 验收标准
1. Tier1 命中资源的播放请求返回 302 且客户端秒开；
2. Tier2 未转存资源在 `stream_wait_ms` 内就绪则 302，超时则返回占位流且重试成功；
3. 失效资源播放不返回死链；被自动置不可播并触发回退；
4. 同一番号并发播放只创建一个转存任务；
5. TTL 到期后临时文件被删除、资源置不可播，且**白名单目录外零删除**；
6. 任何写库路径都不单独修改 `is_available`（必须经 `recomputeAvailability`）。

## 风险 / 备注
- 占位流方案是客户端兼容性与用户体验的折中，需在 P6 与真实客户端联调确认。
- Janitor 删除为异步，连续删除需间隔并校验返回值。
