# P6 · Emby 协议模拟

> 上级计划：[`../plan.md`](../plan.md) ｜ 需求：[`../spec.md`](../spec.md) FR-EMBY
> 对应设计：`PROJECT_SPEC` §9.1–§9.4；接口清单以 `references/emby_protocol_endpoints.md` 为准

## 目标
完整模拟 Emby 4.8+ 核心协议，使 Infuse / VidHub / Kodi / Emby Web 可无感接入，并覆盖搜索、图片、播放与进度。

## 前置依赖
P5（[`05-playback-115.md`](./05-playback-115.md)）提供播放与进度落库能力。

## 任务清单

| ID | 任务 | 优先级 |
|---|---|---|
| T-601 | Emby 路由骨架 + `GET /emby/system/info/public` 与 `/system/info` | P0 |
| T-602 | `POST /emby/users/authenticatebyname` → 建 `auth_sessions`，返回 AccessToken（DB 存 SHA-256） | P0 |
| T-603 | 鉴权中间件：除 public/认证外 `/emby/*` 均需 `X-Emby-Token`/`api_key` | P0 |
| T-604 | `GET /emby/users/me` 与 `/users/{id}` 返回 User DTO（含 Policy） | P0 |
| T-605 | `GET /emby/library/mediafolders` → 从 `libraries` 表输出 6 大分类 | P0 |
| T-606 | `GET /emby/users/{uid}/items`：`ParentId`/`IncludeItemTypes`/`Recursive`/`SortBy`/`StartIndex`/`Limit`/`SearchTerm` | P0 |
| T-607 | 搜索逻辑：本地命中（code/title/title_zh）；未命中触发在线搜索并降级（空列表非 5xx） | P0 |
| T-608 | `GET /emby/users/{uid}/items/{id}` 详情：Movie DTO + `MediaSources[]` + `UserData` + `ImageTags` | P0 |
| T-609 | `GET /emby/items/{id}/images/{Primary\|Backdrop}`：代理磁盘缓存 + ETag + 占位图 | P0 |
| T-610 | `GET /emby/videos/{id}/stream[.{container}]`：接入 P5 Stream Resolver（含 `MediaSourceId`） | P0 |
| T-611 | 会话上报：`/sessions/playing`、`/playing/progress`、`/playing/stopped`（Ticks 换算、90% 判完播） | P0 |
| T-612 | 收藏/已看：`favoriteitems`、`playeditems`；`/items/resume`、`/shows/nextup`、`/items/latest`、`/items/counts` | P1 |
| T-613 | 兼容性联调：Infuse / VidHub 实际登录、浏览、播放、续播全流程 | P0 |

## 交付物
- `internal/emby/`（`router.go`/`handshake.go`/`library.go`/`search.go`/`images.go`/`middleware.go`/`playback.go`/`session.go`）
- 至少两款真实客户端可完成"登录→浏览→播放→续播"

## 验收标准
1. Infuse 与 VidHub 能成功登录并展示分类海报墙；
2. 海报与背景图正常显示（不白屏，源站失效时有占位图）；
3. 详情页可见多个 `MediaSources` 版本并可切换；
4. 点击播放能 302 到 115 CDN 并起播；
5. 播放进度写入 `user_progress`，重新进入可"继续观看"；
6. 搜索本地未收录番号时联网补全返回结果；网络失败时返回空列表而非报错。

## 风险 / 备注
- 各客户端对 Emby 字段宽容度不同，需以真实客户端抓包为准微调；
- 仅支持 DirectPlay/DirectStream，不实现转码，浏览器播放 HEVC 可能失败；
- `ImageTags` 必须填 hash，否则客户端不请求图片。
