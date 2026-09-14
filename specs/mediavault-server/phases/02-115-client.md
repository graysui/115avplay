# P2 · 115 OpenAPI 客户端

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-STREAM
> 对应设计：`PROJECT_SPEC` §8；字段与坑点以 `references/115_openapi_field_spec.md` 为准

## 目标
封装一个**稳定、可观测、带限流与重试**的 115 官方 OpenAPI 客户端，覆盖目录扫描、离线转存、删除与取直链。

> ⚠️ 本阶段是**全项目唯一未验证的外部链路**，应尽早开始端到端验证。

## 前置依赖
P1（[`01-data-layer.md`](./01-data-layer.md)）提供 `system_settings` 存储 token。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-201 | HTTP 客户端基座：统一请求、签名、UA、超时、Retry-After 解析 | P0 |
| T-202 | OAuth2：`access_token` 自动刷新、并发单飞、加密持久化、失效告警钩子 | P0 |
| T-203 | `get_file_info` / `get_file_parent_id` / `search_file`（按字段规范实现，避免 cookie 侧陷阱） | P0 |
| T-204 | `list_files` / `list_files_recursive`（`type=4&cur=0` 递归、翻页、去重、upt 截断） | P0 |
| T-205 | 取直链 `downurl` + single-flight 缓存 + 失败识别（file not found / errno） | P0 |
| T-206 | 离线转存：`add_task_bt`（btih）与 `add_task_urls`（ed2k）+ 任务状态轮询 | P0 |
| T-207 | 物理删除 `ufile/delete`（异步语义、间隔校验）与回收站操作 | P1 |
| T-208 | 限流器：并发槽 + 退避；请求日志与耗时指标 | P1 |
| T-209 | 契约测试（基于 `references/115_openapi_field_spec.md` 的真实响应样例，mock HTTP） | P1 |

## 交付物
- `internal/client115/` 完整客户端（`client.go`/`oauth.go`/`offline.go`/`tree_scan.go`/`delete.go`/`downurl.go`）
- 可运行的冒烟脚本：给定 pick_code 取到直链、给定磁力创建转存任务

## 验收标准
1. 使用真实 token 能列出指定 CID 的目录与视频；
2. 给定 `pick_code` 能取到 115 直链（HTTP 200 且可 Range 请求）；
3. 给定一个测试磁力/ed2k 能成功创建转存任务并轮询到就绪；
4. token 过期时自动刷新，连续失败触发告警且不崩溃；
5. 触发 115 限流时按 `Retry-After` 退避，不产生请求风暴。

## 风险 / 备注
- 字段命名跨接口不一致（`fid`/`file_id`、`cid` 语义差异），**必须对照 `115_openapi_field_spec.md` 的 Part C 对照表**，禁止臆测。
- 文件项 `cid` 是父目录而非自身；取父目录必须用 `get_file_parent_id`.
- 上传协议存在 `token invalid` 的编码类坑点，本项目转存走离线接口，暂不涉及上传。
