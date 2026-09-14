# P7 · 管理后台（REST API + Web 控制台）

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-ADMIN
> 对应设计：`PROJECT_SPEC` §10 / §10.1 / §10.2

## 目标
提供管理员可用的 REST API 与 Vue 单页控制台，覆盖统计、配置、番号管理、刮削/榜单/同步触发与实时日志。

## 前置依赖
P5（[`05-playback-115.md`](./05-playback-115.md)）与 P4（[`04-scraping.md`](./04-scraping.md)）的任务控制接口。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-701 | 管理员登录/登出：`POST /api/admin/login`（argon2id）、`/logout`，复用 `auth_sessions` | P0 |
| T-702 | `GET /api/stats`：`is_enriched` 0/1/2/3 分布与总量 | P0 |
| T-703 | `GET/PUT /api/config`：读写 `system_settings`，secret 字段脱敏 | P0 |
| T-704 | `GET /api/movies`（分页/过滤）、`GET/PUT/DELETE /api/movies/{code}` | P0 |
| T-705 | `POST /api/scraper/run`（日期范围/含失败/并发/延时/代理）+ `GET /api/scraper/status` | P0 |
| T-706 | `POST /api/rankings/run`（board/year）、`POST /api/sync30d/run` | P0 |
| T-707 | `GET /api/tasks/{id}`：异步任务注册与查询（`job_registry.go`） | P1 |
| T-708 | `GET /api/logs/stream`：WebSocket 实时日志推送（订阅 log_hub） | P0 |
| T-709 | 前端工程：Vue 3 + Vite + TailwindCSS + Pinia 脚手架与构建 | P1 |
| T-710 | 页面：Dashboard / Movies / Scraper / Rankings / Settings / Logs | P1 |
| T-711 | `go:embed` 打包 `web/dist` 为单二进制静态资源 | P0 |

## 交付物
- 完整管理端 REST API（见 PROJECT_SPEC §10.1 的 17 个端点）
- 可登录、可操作、可看日志的 Web 控制台，随二进制一起分发

## 验收标准
1. 未登录访问管理 API 被拒绝；
2. 仪表盘统计数字与数据库实际分布一致；
3. 指定日期范围点击"全量刮削"能启动任务并在页面看到进度；
4. 周榜/月榜/TOP250 按钮可触发抓取；
5. 实时日志面板可滚动看到服务端日志；
6. 修改配置（TTL/并发/代理）后立即生效并持久化；
7. 前端静态资源已嵌入二进制，无需单独部署。

## 风险 / 备注
- secret 配置（115 Cookie/Token）必须脱敏返回，且加密存储；
- 长任务（刮削/同步）必须异步化 + 进度查询，禁止同步阻塞 HTTP 请求。
