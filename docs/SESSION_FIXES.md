# MediaVault 排障与改造记录（本次会话）

> 本文记录本次会话中定位并修复的所有问题、实现的新功能及其解决办法，供后续维护与复盘使用。
> 记录格式：**现象 → 根因 → 解决办法 → 验证**。

---

## 目录

1. [构建与部署](#1-构建与部署)
2. [115 授权与扫码](#2-115-授权与扫码)
3. [115 目录树扫描（永久媒体库）](#3-115-目录树扫描永久媒体库)
4. [播放链路（直链 / 转存）](#4-播放链路直链--转存)
5. [封面图片](#5-封面图片)
6. [JavDB 刮削](#6-javdb-刮削)
7. [旧数据库迁移](#7-旧数据库迁移)
8. [Emby 协议兼容](#8-emby-协议兼容)
9. [管理后台前端](#9-管理后台前端)
10. [日志模块](#10-日志模块)
11. [榜单虚拟媒体库](#11-榜单虚拟媒体库)
12. [数据库口径与状态说明](#12-数据库口径与状态说明)
13. [关键参数速查](#13-关键参数速查)

---

## 1. 构建与部署

### 1.1 Docker 构建失败：Go 版本不匹配
- **现象**：`go.mod` 要求 `go 1.26.5`，基础镜像为 `golang:1.24-alpine`，构建报错。
- **解决**：`Dockerfile` 基础镜像改为 `golang:1.26-alpine`。

### 1.2 模块下载超时
- **现象**：`proxy.golang.org` 连接超时。
- **解决**：`Dockerfile` 增加
  ```
  ARG GOPROXY=https://goproxy.cn,direct
  ENV GOPROXY=${GOPROXY}
  ```

### 1.3 构建上下文过大
- **现象**：`data/`（含 370MB 数据库）被 `COPY . .` 打包进镜像。
- **解决**：新增 `.dockerignore`，排除 `data/`、`.git`、`bin/`、`node_modules/` 等。

### 1.4 容器数据卷与启动
```bash
docker build -t mediavault:latest .
docker run -d --name mediavault --restart unless-stopped \
  -p 8096:8096 -v /opt/mediavault/data:/app/data \
  -e MV_ADMIN_PASSWORD='...' mediavault:latest
```

---

## 2. 115 授权与扫码

### 2.1 `115 auth client not configured`
- **根因**：`cmd/server/main.go` 把 `AuthClient` 以 `nil` 传入管理端，115 认证从未接线。
- **解决**：
  - 启动时创建 `client115.Client` + `AuthClient` 并注入服务与 API；
  - 从数据库解密恢复已绑定 Token（重启不掉登录）。

### 2.2 PKCE 参数错误
- **现象**：115 返回 `40140102 code_challenge_method 必须是 sha256/sha1/md5`。
- **根因**：代码用了标准 PKCE 的 `"S256"`。
- **解决**：改为 `code_challenge_method=sha256`，challenge 使用标准 Base64（与参考实现一致）。

### 2.3 OAuth 域名错误
- **现象**：请求打到 `proapi.115.com`，返回 `access_token 格式错误`。
- **根因**：OAuth 端点在 `passportapi.115.com`。
- **解决**：客户端新增独立 `AuthBaseURL`（默认 `passportapi.115.com`），`authDeviceCode`/`deviceCodeToToken`/`refreshToken` 走正确域名；文件/离线接口仍走 `proapi`。

### 2.4 二维码不显示 / 有效期 0 秒
- **根因**：115 `authDeviceCode` 返回的是 `uid/time/sign`，**不是** `device_code/user_code/qrcode_url/expires_in`。
- **解决**：
  - 解析 `uid/time/sign`；
  - 若返回的是扫码落地页（`https://115.com/scan/dg-...`），在**服务端用 `skip2/go-qrcode` 生成二维码 PNG**，以 data URI 返回前端；
  - 若返回图片地址则服务端抓取转 data URI；
  - 有效期缺省 300 秒。

### 2.5 `115 App Secret` 不需要
- **结论**：设备码流程只用 `client_id`；代码中 `clientSecret` 从未被使用。
- **解决**：**移除前端 App Secret 输入框**，后端删除该字段，仅保留 `MV_115_CLIENT_ID`。

### 2.6 `40140109 访问权限已停用`
- **结论**：**115 平台侧问题**（应用/账号未开通开放接口权限），非代码问题。
- **处理**：需在 <https://open.115.com> 申请/恢复权限；接口错误已原样透传给前端。

---

## 3. 115 目录树扫描（永久媒体库）

### 3.1 只扫描到部分文件（目录树取不全）
- **根因**：OpenAPI `/open/ufile/files?type=4&cur=0` 不是真正的递归，只返回一层/部分。
- **解决（方案 A）**：实现 **BFS 逐层遍历**（列目录 → 子目录入队），带分页、visited 去重、限流重试。
- **解决（方案 B，推荐）**：采用 p115client 的 **"穿透式"接口**：
  | 接口 | 作用 |
  |---|---|
  | `GET webapi.115.com/files/file?file_id=<CID>` | CID → pickcode |
  | `GET webapi.115.com/files/downfolders?pickcode=..&page=N&per_page=5000` | 整个子树目录 |
  | `GET webapi.115.com/files/downfiles?pickcode=..&page=N&per_page=5000` | 整个子树文件 |
  整棵树几次分页取完（官方 benchmark：13k 目录 + 108k 文件约 1~3 秒）。

### 3.2 每个目录只保留最大视频
- **需求**：末级目录只记录最大的视频文件的 pickcode，其余过滤。
- **解决**：文件按父目录分组，每个目录取 `size` 最大且 ≥100MiB 的一个。

### 3.3 多 CID 只扫描第一个
- **根因**：`TriggerScan` 只取 `existing_scan_cids` 的第一项。
- **解决**：一次任务遍历全部根目录（4 个根约 20 秒）。

### 3.4 OpenAPI 批量列举被 WAF 拦截（HTTP 405）
- **现象**：`405` + 阿里云风格 HTML 错误页。
- **解决**：改用 Cookie 网页接口（上文方案 B）；新增 **115 网页 Cookie** 配置项。

### 3.5 崩溃重启后任务残留
- **现象**：`running` 任务残留，导致新扫描被去重阻塞。
- **解决**：启动时 `FailOrphanedJobs` 清理非终态任务。

### 3.6 扫描任务 panic 拖垮进程
- **根因**：`result["assets"]` 未初始化 + 后台 goroutine 无 recover。
- **解决**：初始化 map 字段，并为后台任务加 `recover` 保护。

### 3.7 重复永久资产
- **根因**：BFS 用数字 file id、穿透接口用 pickcode，同一文件建了两条。
- **解决**：统一以 **pickcode** 为主键（确定性 asset id），清理历史重复（0 重复）。

### 3.8 番号识别率极低（目录名带后缀）
- **根因**：`NormalizeCode` 正则**锚定** `^...$`，`JUFE-101-U`、`PRED-856ch`、`FC2PPV-...-C` 全部匹配失败。
- **解决**：按规范改为**非锚定 + 边界检查**算法，并增加预处理：
  - 去掉字幕/无码后缀：`-C/-U/-UC/-ch/-c`；
  - 去掉垃圾域名：`kckc13.com@SDJS-156` → `SDJS-156`。

---

## 4. 播放链路（直链 / 转存）

### 4.1 `add_task_bt 参数错误`
- **根因**：115 OpenAPI 离线任务只有 `add_task_urls`，参数名是 **`urls`**（不是 `url[0]`），`add_task_bt` 不可用。
- **解决**：磁力与 ed2k 统一走 `add_task_urls` + `urls`，并正确解析返回的 JSON 数组。

### 4.2 `access_token 无效 (40140125)`
- **根因**：OAuth access_token 过期且从不自动刷新。
- **解决**：任何 115 API 遇到鉴权错误时**自动 `RefreshToken` 并重试一次**，刷新后的 token **加密持久化**。
- **补充修复（后续暴露）**：刷新逻辑最初只写在了 `doRequestWithAuthRetry` 里，**只有 `GetDownloadURL` / `AddURLTask` 使用它**；`CreateFolder`（`/open/folder/add`）等其它 OpenAPI 调用走普通 `DoRequest`，仍报 `40140125`，造成「有的能播、有的转存失败」。现已把**刷新重试下沉到 `DoRequest` 本身**，覆盖全部 OpenAPI 调用（含递归保护 `refreshing` 与请求体缓冲以便重试）。
  - 实测：冷资源 `IPX-515` / `PASN-038` 转存全链路成功（`已创建临时目录` → `已提交 115 离线任务` → `转存完成`）。

### 4.3 downurl 解析失败
- **根因**：`/open/ufile/downurl` 返回的 `data.<id>.url` 是**嵌套对象** `{"url":"..."}`，不是字符串。
- **解决**：兼容字符串/对象两种形态。

### 4.4 302 直链 403 `invalid signature`
- **根因**：用通用 UA 申请直链会得到带 `f=1` 的链接（**要求相同 User-Agent 才能访问**），播放器无法满足。
- **解决**：115 客户端 User-Agent 改为 **`115disk/2.0`**，返回的直链 `f` 为空（无 UA 锁），任意播放器可用。
  - 实测：浏览器 UA / 无 UA 请求均返回 `206`。

### 4.5 冷资源首次播放返回“准备中”
- **说明**：这是设计行为（8 秒等待），后台会持续转存，完成后自动注册临时资产（Tier2 秒播）。
- **日志**：`已提交 115 离线任务` → `转存完成，临时资产已就绪`。

### 4.6 流媒体路径被重复拼接 `/emby/emby/...`
- **根因**：返回的 `DirectStreamUrl` 带了 `/emby` 前缀，客户端再拼一次。
- **解决**：返回相对路径 `/videos/{id}/stream?...`。

### 4.7 图片/流媒体请求 401
- **根因**：Emby 客户端对图片/流媒体请求**不带 Token**。
- **解决**：`items/{id}/images/*` 与 `videos/{id}/stream*` 设为**公开路由**（流媒体回退到第一个可用用户）。

### 4.8 `SortBy` 400 `invalid sortby field`
- **根因**：白名单过严（只允许 3 个字段），VidHub 发送的 `Random`/`DatePlayed` 等被拒。
- **解决**：映射全部常见 Emby 排序字段，未知字段回退默认（不再 400）。

### 4.9 快进会重新申请直链 / 直链未缓存
- **现象**：播放器每次拖动进度条、请求分片都会重新打 `/stream`，每次都向 115 重新申请一条新直链（每次 302 前 0.3~2.5s 延迟），快进卡顿且浪费取链接口配额。
- **解决**：新增**直链缓存**（`client115`）——按 `pick_code` 缓存直链，有效期取直链自带 `t`（过期时间戳）**减 60 秒**，未知则默认 5 分钟。
  - 实测：同一版本连续请求 `0.67s → 0.003s`（第 2/3 次命中缓存，未请求 115）。
  - 说明：115 CDN 直链本身支持 **HTTP Range**，同一有效直链上拖动无需重新解析；仅当直链过期或切换版本时才重新申请。

---

## 5. 封面图片

### 5.1 JavDB 封面是加密数据
- **根因**：App API 返回 `tp.spfcas.com`（**加密后的随机字节**，非图片）。
- **解决**：`NormalizeImageURL` 改写为网页版 CDN **`c0.jdbstatic.com`**（标准 JPEG），在客户端、管理端图片代理、Emby 图片处理三处生效。

### 5.2 离线库图床需要 Referer
- **根因**：`tu.djhdhs.us` 等图床无 Referer 返回 `403`。
- **解决**：抓图时自动带 `Referer: <图片域名>/`。

### 5.3 封面加载慢 / 翻页后没封面
- **根因**：
  - 旧库图床大量失效（502/超时），旧逻辑**串行重试**，最坏 24 秒；
  - 失败占位图被缓存 **24 小时**，当天不再重试。
- **解决**：
  - **并行竞速**多个候选（封面/海报/预览图），整体 6 秒上限，最快者胜；
  - 占位图 `Cache-Control` 改为 **60 秒**（快速重试）；
  - 成功图片磁盘缓存 7 天；单次抓取超时 6 秒、候选上限 4。
- **实测**：失效图床影片由 24 秒降至 **0.3~1.3 秒**返回真实图片。

### 5.4 FC2/国产/刮削失败：沿用离线库封面
- **解决**：启动时 `BackfillCoversFromPreview`，把离线库 `preview_images` 的第一张**回填为 `cover_url`**（仅填空值，不覆盖 JavDB 封面）。
- **效果**：一次回填 103,640 张；`缺封面` 由 68,769 降至 6。

---

## 6. JavDB 刮削

### 6.1 榜单/刮削解析 `success` 字段失败
- **根因**：JavDB 返回 `"success": 1`（数字），客户端期待 `bool`。
- **解决**：兼容 `bool/数字/字符串`。

### 6.2 详情接口路径过时
- **根因**：`/api/v1/movies/{id}` 已 404，实际是 `/api/v2/movies/{id}`，数据在 `data.movie`，`type` 为数字。
- **解决**：修正路径与字段解析（演员/标签为对象数组、`score` 为字符串、`duration` 为分钟）。

### 6.3 磁力字段变更
- **根因**：新版返回 `hash/size/cnsub/hd`，旧代码找 `magnet_url/size_bytes`。
- **解决**：用 `hash` 拼磁力链并映射字段。

### 6.4 全部刮成 `partial`
- **根因**：完整度判定要求 `description_zh`，而 **JavDB v2 根本不提供中文字段**（`title_zh/description_zh/review` 均为 null）。
- **解决**：完整度改为 **标题 + 封面** 即视为完整；刮到的影片变 `success`。

### 6.5 候选数不对 / 待刮削口径
- **解决**：
  - `TriggerScrape` 返回**真实候选数**（受 limit 封顶）；
  - 候选 = 未成功刮削（`idle/transient/partial/not_found`）且非 `exempt`；
  - 状态接口支持 `date_field/start_date/end_date`，统计随日期区间变化；
  - 新增「缺少字段」明细（缺封面/缺中文标题/缺简介）。

### 6.6 FC2/素人、国产不刮削
- **说明**：这两类 `scrape_policy=exempt`，不在候选内；封面来自离线库预览图回填。

### 6.7 新增影片仍需刮削 + sync30d 联动
- **解决**：
  - 启动时**一次性**把已具备离线元数据的旧库标记为已刮削（`legacy_scrape_normalized` 标记，177,036 条）；
  - 新增入库影片保持 `idle`，会被刮削；
  - **`sync30d` 完成后自动触发「仅 idle」刮削**（`OnlyIdle`），日程里的 sync30d 同样联动；
  - JavDB 有则覆盖，没有则保留离线库封面/标题。

### 6.8 JavDB 登录与 Cookie
- **解决**：
  - `POST /api/v1/sessions` 账号密码登录，token 用作 `Authorization: Bearer`；
  - 客户端**捕获 `Set-Cookie`** 并在后续请求带上 Cookie；
  - token 与 cookie 均加密持久化（`javdb_token` / `javdb_cookie`）。

---

## 7. 旧数据库迁移

### 7.1 迁移外键顺序错误
- **现象**：`drop legacy tables: FOREIGN KEY constraint failed`。
- **根因**：旧库 `offline_magnets` 外键指向 `offline_movies`，删除顺序错误（先删父表）。
- **解决**：**先删子表 `_legacy_magnets`，再删 `_legacy_movies`**。

### 7.2 多语句 Exec 失败
- **解决**：`ALTER TABLE a; ALTER TABLE b;` 拆成单条执行。

### 7.3 WAL 覆盖导致旧库“没识别”
- **现象**：把旧库放到 `mediavault.db` 后仍显示空。
- **根因**：**容器运行中复制**，旧的 `-wal` 把旧库盖住；停止容器时 WAL 又 checkpoint 覆盖了旧库。
- **正确步骤**：
  ```bash
  docker stop mediavault
  rm -f /opt/mediavault/data/mediavault.db-wal /opt/mediavault/data/mediavault.db-shm
  cp 旧库.db /opt/mediavault/data/mediavault.db
  docker start mediavault   # 自动识别 legacy-v0 并迁移（自动备份）
  ```

---

## 8. Emby 协议兼容

| 问题 | 解决 |
|---|---|
| 图片请求 401 | 图片路由公开 |
| 流媒体 `f=1` 403 | 115 UA 改 `115disk/2.0` 取无锁直链 |
| `invalid sortby field` | 全字段映射 + 未知回退 |
| `/emby/emby/...` 重复前缀 | 返回相对路径 |
| `Items/Resume` 500 | SQLite NULL 扫描（`official_title`）→ `COALESCE` 修复 |
| 背景图取不到 | `preview_images` 是逗号分隔字符串，解析兼容 JSON/逗号 |
| `Similar`/`Tags`/`Genres`… 404 | Emby 可选项，客户端会忽略（不影响播放） |
| **首页各媒体库内容相同** | Emby 首页行用的是 `Items/Latest?ParentId=…`，而 `GetLatest` **忽略了 `ParentId`**，永远返回全库最新；抽出 `libraryPredicateClause` 供 `GetItems`/`GetLatest` 共用 |
| **FC2/素人 库为空** | 谓词分类写成 `FC2`，实际离线库分类是 `FC2/素人` → 修正为 `IN ('FC2','FC2/素人','素人')` |

---

## 9. 管理后台前端

### 9.1 页面崩溃（榜单与日程）
- **根因**：空列表返回 `null`，模板 `schedulesList.length` 抛异常。
- **解决**：后端空列表返回 `[]`；前端 `|| []` 兜底。

### 9.2 创建用户 / 修改密码 / 重置密码不可用
- **解决**：补齐三个弹窗与对应逻辑（原先引用但未实现）。

### 9.3 「新增日程」是浏览器 prompt
- **解决**：改为**网页弹窗**（名称/类型/周期/时区/时间/规则预览）。

### 9.4 保存配置 400
- **根因**：清空数字输入框会发送 `""`，校验器拒绝。
- **解决**：后端空值视为「未设置」；前端提供默认值并在保存时跳过空数字字段。

### 9.5 Tailwind CDN 警告
- **解决**：移除 `cdn.tailwindcss.com`，改为**预编译内联 CSS**（配置见 `web/tailwind.config.js`）。

### 9.6 刷新回到仪表盘
- **解决**：用 `localStorage('mv_tab')` 记忆当前页。

### 9.7 日志页整页滚动
- **解决**：日志窗口固定高度 `h-[calc(100vh-220px)]` 自身滚动并自动滚到底部。

---

## 10. 日志模块

- **中文请求日志**：记录方法/路径/状态/耗时/来源，token 自动隐藏；
- **常规轮询请求降级 DEBUG**（`/api/v1/scraper/status`、`/stats`、`/115/status`、`/javdb/status`、`/system/schema`、图片请求、健康检查），出错仍显示；
- **播放全链路日志**：解析 → 选版（Tier）→ 命中 → 302/准备中/失败原因；
- **刮削进度**：逐条日志 + 每 10 条汇总，前端进度卡片实时刷新（每 3 秒）；
- **永久媒体库扫描进度**：目录遍历进度 + 完成统计；
- **错误属性转字符串**，避免前端出现 `[object Object]`；
- 日志查看器显示详细属性与本地时间。

---

## 11. 榜单虚拟媒体库

- 新增 `ranking_entries` 表（board/rank/code/title）与三个媒体库：
  **周榜 / 月榜 / TOP250**（`sort_order` 1-3，位于原有 6 个分类之上）；
- 榜单同步（`executeRankings`）写入该榜单条目；
- 库查询 = 榜单番号 ∩ 数据库存在 ∩ **有可用磁力**（离线库 + JavDB）；
- `TriggerRankings` 默认同步全部三榜，日程同步三榜，TOP250 翻页拉全 250 条；
- 幂等启动迁移 `EnsureRankingLibraries`（重建 `libraries` 的 CHECK、补种媒体库）。
- **列表与首页行统一过滤**：`GetItems`（点进库）与 `GetLatest`（首页横向行）现在共用 `libraryPredicateClause`，保证每个库（含三个榜单库）首页展示各自内容。
- **注意**：三个榜单库的内容依赖**榜单同步完成**；`TriggerRankings` 默认同步全部三榜，TOP250 会翻页拉满 250 条。实测 `ranking_entries`：`weekly=60 / monthly=60 / top250=250`。

---

## 12. 数据库口径与状态说明

| 术语 | 口径 |
|---|---|
| 待刮削 | `scrape_status='idle'`（新增未刮削，已排除 `exempt`） |
| 元数据完整 | `scrape_status='success'`（标题 + 封面即可，JavDB 无中文简介不扣分） |
| 缺封面 | `cover_url` 为空（回填预览图后仅剩个位数） |
| 永久媒体库 | 扫描建立的 `cloud_assets(source_type='permanent')`，按 pickcode 去重 |
| 临时资产 | 转存就绪的 `cloud_assets(source_type='temporary')`，带 TTL |

---

## 13. 关键参数速查

| 环境变量 | 说明 |
|---|---|
| `MV_ADMIN_PASSWORD` | 初始管理员密码 |
| `MV_115_CLIENT_ID` | 115 开放平台 App ID（扫码必需） |
| `MV_LOG_LEVEL` | `DEBUG/INFO/WARN/ERROR` |
| `MV_PUBLIC_URL` | 反向代理公网地址 |

| 数据库设置（管理后台） | 说明 |
|---|---|
| `115_client_id` | 115 App ID（前端可改） |
| `115_cookie` | 115 网页 Cookie（目录树扫描用） |
| `temp_transfer_cid` | 临时转存目录 CID |
| `existing_scan_cids` | 永久媒体库根目录 CID 列表 |
| `javdb_token` / `javdb_cookie` | JavDB 登录凭据（自动保存） |
| `legacy_scrape_normalized` | 旧库是否已标记为已刮削（一次性） |

| 115 User-Agent | 结果 |
|---|---|
| `MediaVault/1.0` 等 | 直链 `f=1`（UA 锁，播放器不可用） |
| **`115disk/2.0`** | 直链 `f=""`（无锁，任意播放器可播） |

---

*文档生成时间：本次会话；适用于 `115avplay` / MediaVault Go 虚拟媒体服务器。*
