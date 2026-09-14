# P3 · 离线数据摄取（30 天增量）

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-INGEST
> 对应设计：`PROJECT_SPEC` §5.1 / §5.3 / §5.4

## 目标
实现 AVDB-Only 30 天增量包的**自动探测、下载、AES-256-GCM 解密、清洗过滤与增量入库**。

## 前置依赖
P1（[`01-data-layer.md`](./01-data-layer.md)）提供数据层与查询封装。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-301 | GitHub Releases API 探测最新 tag，流式下载 30D 包（支持镜像回退） | P0 |
| T-302 | AES-256-GCM 解密器：解析 `avdb-resource-library.json`（salt/nonce/tag/iterations）→ PBKDF2-HMAC-SHA256 → GCM 解密 → 内层 ZIP 取 CSV | P0 |
| T-303 | CSV 解析：表头校验、番号归一化（FC2 统一 `FC2-PPV-*`） | P0 |
| T-304 | 分类过滤：丢弃 VR/欧美/写真；FC2/国产置 `is_enriched=2` | P0 |
| T-305 | `info_hash` 提取与 `resource_kind` 判定（btih→urihash；ed2k→SHA1；否则丢弃） | P0 |
| T-306 | 增量对账入库：新番号置 0、新磁力按 `info_hash` 去重、批处理 + 事务 | P0 |
| T-307 | 定时调度（每日 04:00）+ 手动触发接口（供 P7 调用），并发互斥 | P0 |
| T-308 | 同步审计：写入 run 记录（读取/过滤/去重/入库计数、失败原因） | P1 |
| T-309 | 单测：解密测试向量、过滤规则、去重、ed2k 键生成 | P0 |

## 交付物
- `internal/ingestion/`（`daily_30d.go` / `decryptor.go` / `merger.go`）
- 一次完整增量同步的可复现流程与统计报告

## 验收标准
1. 给定加密包能正确解密并提取 CSV（与参考实现结果一致）；
2. VR/欧美/写真 100% 被过滤，FC2/国产被标记 `is_enriched=2`；
3. 重复运行同一批次，`offline_magnets` 不新增重复 `info_hash`；
4. 新番号入库后 `is_enriched=0`，并进入待刮削队列；
5. 同步失败时写入失败原因，且不阻塞服务其他功能。

## 风险 / 备注
- **解密算法为 AES-256-GCM（非 CBC）**，认证失败必须直接拒绝，禁止回退试 CBC。
- 增量包与全量包共用同一解密格式，解密器需两者通用。
- 定时任务需防重入（同一 slot 只运行一个实例）。
