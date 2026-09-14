# P0 · 工程骨架与基础设施

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) §6 NFR
> 对应设计：`PROJECT_SPEC` §3 技术栈、§11 工程目录

## 目标
建立**可编译、可启动、可观测**的最小工程骨架，为后续所有阶段提供地基。

## 前置依赖
无（起点）。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-001 | 初始化 `go.mod`（Go 1.22+），确定 module 路径 | P0 |
| T-002 | 创建目录结构：`cmd/server`、`internal/{api,emby,client115,javdb,ingestion,db,models,services}`、`web`、`data` | P0 |
| T-003 | 配置加载：`system_settings` 表 + 环境变量 + 默认值合并；secret 字段加密读写 | P0 |
| T-004 | 结构化日志（`log/slog` 或 zap）+ `services/log_hub.go` 内存 ring buffer（2000 条，支持订阅） | P0 |
| T-005 | SQLite 连接：WAL 模式、`busy_timeout`、`PRAGMA foreign_keys=ON`、连接池参数 | P0 |
| T-006 | 迁移框架：`schema_meta` 版本表 + 幂等 DDL 执行器（只增不删） | P0 |
| T-007 | HTTP server 骨架（Gin/Echo）+ 优雅关闭 + `/healthz` | P0 |
| T-008 | 构建脚本 `Makefile`（build/run/test/vet）与 `.gitignore` | P1 |
| T-009 | 统一错误与响应包装（管理 API 用） | P1 |

## 交付物
- 可执行 `go run ./cmd/server`，监听端口并返回 `/healthz` 200
- `data/offline_full_merged.db` 可被打开且完成空迁移
- 日志可输出到 stdout 并能被 ring buffer 订阅

## 验收标准
1. `go build ./...` 与 `go vet ./...` 零错误；
2. 首次启动自动创建 `schema_meta` 并记录 `schema_version=1`；
3. `curl /healthz` 返回 200；
4. 重启进程不会重复执行已应用的迁移；
5. 日志级别可由配置控制。

## 风险 / 备注
- **Gin vs Echo**：需在 T-007 定案（本计划默认 Gin，若不合适在 P0 内切换，避免后期返工）。
- SQLite 并发写需注意单写者模型，写操作统一走短事务。
