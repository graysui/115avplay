# P1 · 数据层与模型

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-DATA
> 对应设计：`PROJECT_SPEC` §4.1 / §4.2 / §4.3 / §4.4

## 目标
落地全部 9 张表、模型与查询封装，并具备**旧库兼容迁移**能力。

## 前置依赖
P0（[`00-foundation.md`](./00-foundation.md)）完成迁移框架与 DB 连接。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-101 | `offline_movies` DDL（含 `created_at`/`updated_at`、`is_enriched` 0/1/2/3 语义注释） | P0 |
| T-102 | `offline_magnets` DDL（含 `resource_kind` CHECK、`priority_score`、部分唯一索引 `idx_off_mag_preferred`） | P0 |
| T-103 | 运行时 6 表 DDL：`users`/`auth_sessions`/`user_progress`/`play_sessions`/`libraries`/`system_settings` | P0 |
| T-104 | 初始化数据：6 条 `libraries`、默认 `system_settings`（TTL/并发/代理等 15 项） | P0 |
| T-105 | models 实体：`movie`/`magnet`/`user`/`auth_session`/`play_session`/`progress`/`library`/`setting` | P0 |
| T-106 | 查询封装 `queries.go`：影片分页检索、磁力按番号、可播资源、resume、统计聚合 | P0 |
| T-107 | 旧库兼容：`ALTER TABLE ADD COLUMN` 补齐 `created_at`/`updated_at`，缺失表补齐 | P0 |
| T-108 | 单测：迁移幂等、partial unique index、FK 级联、默认值 | P0 |

## 交付物
- 全部 DDL 与迁移脚本通过 `schema_meta` 版本管理
- 一套类型安全的 model 与 repository 层
- `offline_full_merged.db` 打开后可自动补齐为完整 9 表结构

## 验收标准
1. 对全新空库执行迁移 → 9 表 16 索引全部建立；
2. 对现有 `offline_full_merged.db` 执行迁移 → 仅新增缺失列/表，**原 21 万影片 / 32 万磁力零丢失**；
3. 插入同番号第二条 `is_preferred=1` 被唯一索引拒绝；
4. 删除 `offline_movies` 一行，级联清除 `offline_magnets` 与 `user_progress`；
5. 迁移可重复执行且结果一致。

## 风险 / 备注
- **字段顺序**：DDL 中列尾逗号易错，建议用自动化脚本从 PROJECT_SPEC 的 SQL 块导出并在 CI 校验可执行。
- `category`/`publish_date` 的 NOT NULL 已放宽（`publish_date` 可空），实现时勿回退。
