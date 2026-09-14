# MediaVault 内置媒体服务 (media_library) 深度技术实现与独立架构设计方案

本文档深度剖析 MediaVault 镜像中 `media_library` 插件的底层实现机制，并在此基础上提出**全新的独立项目方案——基于数据库直接构建虚拟媒体库（DB-Driven Virtual Media Server）**，完全摆脱对本地文件系统与 STRM 文件的依赖。

> **文档定位说明（重要）**
>
> 本文档分为两部分：
> - **第一部分**：对原 `media_library` 插件的逆向技术剖析，属**历史参考**，用于理解旧系统机制；
> - **第二部分**：“DB-Driven 虚拟媒体库”的**概念性方案草案**，用于确立方向，**非最终实现规范**。
>
> 新项目的**权威落地设计、数据库 Schema、接口与工程结构，一律以 [`PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md`](./PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md) 为准**。
>
> 特别说明：第二部分§3 提出的 `virtual_media_items` / `virtual_media_streams` / `virtual_user_data` 为**示意表名**，实际实现分别对应主规范的 `offline_movies` / `offline_magnets` / `user_progress`；第二部分“下一步讨论方向”中的技术选型疑问已在主规范中定案（Go 单二进制）。

---

## 第一部分：原插件 `media_library` 具体实现技术拆解

原 `media_library` 插件由数据模型、45 个业务服务子模块以及两套 REST 路由共同构成，实现了从元数据、流媒体到客户端协议兼容的完整自闭环。

```
                       ┌──────────────────────────────────────────────┐
                       │               客户端与终端播放层               │
                       │   Web 播放器  │  Infuse / VidHub  │ 外部播放器 │
                       └──────────────────────┬───────────────────────┘
                                              │
                     ┌────────────────────────┴────────────────────────┐
                     ▼                                                 ▼
          【REST API 管理路由】                               【Emby 兼容协议路由】
        /api/v1/media-library/...                                /emby/...
                     │                                                 │
                     └────────────────────────┬────────────────────────┘
                                              ▼
       ┌────────────────────────────────────────────────────────────────────────────────┐
       │                   核心服务层 (app/services/media_library/)                     │
       │                                                                                │
       │  ┌───────────────────────┐  ┌───────────────────────┐  ┌────────────────────┐  │
       │  │  扫描与刮削流水线    │  │  流媒体与 HLS 实时转码│  │  蓝光/DVD 原盘直读 │  │
       │  │  scanner / probe     │  │  transcode (RemoteIn) │  │  mv-disc-reader    │  │
       │  └───────────────────────┘  └───────────────────────┘  └────────────────────┘  │
       │  ┌───────────────────────┐  ┌───────────────────────┐  ┌────────────────────┐  │
       │  │  声纹片头片尾识别    │  │  弹弹play 弹幕服务    │  │  外部播放器唤起    │  │
       │  │  intro_detect (FFT)  │  │  danmaku (Dandanplay) │  │  external_players  │  │
       │  └───────────────────────┘  └───────────────────────┘  └────────────────────┘  │
       └──────────────────────────────────────┬─────────────────────────────────────────┘
                                              ▼
       ┌────────────────────────────────────────────────────────────────────────────────┐
       │                   数据持久层 (app/models/media_library.py)                     │
       │    MediaLibrary  │  MediaLibraryItem  │  MediaLibrarySource  │  UserData       │
       │    EntityIndex   │  DiscProgress      │  Session             │  ScanTask       │
       └────────────────────────────────────────────────────────────────────────────────┘
```

---

### 1. 数据模型体系架构 (`app/models/media_library.py`)

系统基于 SQLAlchemy 2.0 声明式模型，采用标准的树状继承与多对一关系：

| 模型类名 | 映射表名 | 核心字段说明 | 设计意图与特性 |
| :--- | :--- | :--- | :--- |
| **`MediaLibrary`** | `media_libraries` | `id`, `name`, `library_type`, `source_type`, `storage_slug`, `root_paths`, `allowed_user_ids`, `scan_status`, `scan_token` | 媒体库根定义，支持 Movie/Series/Audiobook，支持按用户权限隔离多库 |
| **`MediaLibraryItem`** | `media_library_items` | `id`, `library_id`, `parent_id`, `identity_key`, `kind`, `title`, `title_initials`, `year`, `season`, `episode`, `tmdb_id`, `playback_markers`, `detected_markers`, `metadata_info` | 核心影视实体，树状结构（Series -> Season -> Episode）；`title_initials` 存储拼音首字母用于秒级拼音搜索；`detected_markers` 记录片头片尾秒级刻度 |
| **`MediaLibrarySource`** | `media_library_sources` | `id`, `library_id`, `item_id`, `source_key`, `path`, `stream_url`, `container`, `size`, `probe_info`, `media_streams`, `subtitles` | 对应物理媒体文件或 302 播放直链；记录 ffprobe 提取的视频流、音频流编码及内嵌/外挂字幕 |
| **`MediaLibraryUserData`** | `media_library_user_data` | `user_id`, `item_id`, `position_ticks`, `played`, `favorite`, `last_played_at` | 维护每个独立用户的播放断点（以 Emby 100ns Ticks 为单位）、已看状态、红心收藏 |
| **`MediaLibraryEntityIndex`**| `media_library_entity_index`| `item_id`, `entity_key` | 倒排索引表（演职员、导演、流派、标签），实现按演员/导演聚合查片的毫秒响应 |
| **`MediaLibraryDiscProgress`**| `media_library_disc_progress`| `user_id`, `source_id`, `title_id`, `fingerprint`, `position_ticks`, `played` | 蓝光原盘/DVD 播放进度表，记录当前播放的 Title 序号与帧时间戳 |
| **`MediaLibrarySession`** | `media_library_sessions` | `id`, `user_id`, `token_hash`, `device_name`, `client_name`, `device_id`, `expires_at` | 客户端播放 Session 票据与鉴权生命周期追踪 |

---

### 2. 核心子系统的具体实现机制

#### (1) 媒体扫描与 Sidecar 零请求解析 (`scanner.py`, `sidecars.py`, `probe.py`)
- **双引擎访问**：抽象了 `cloud_files.py`（针对 115/OpenList 云端目录 API）与 `local_files.py`（本地文件 POSIX 接口）。
- **Sidecar 优先规则**：优先嗅探视频同级目录中的 `tvshow.nfo`、`season.nfo`、`<name>.nfo`、`poster.jpg`、`fanart.jpg` 以及外挂字幕（`.srt`, `.ass`, `.vtt`, `.sup`），无需消耗外部 TMDB API 配额。
- **拼音索引自生成**：入库时调用 `utils.pinyin` 自动推导 `title_initials`（如《黑神话：悟空》-> `HSHWK`），构建拼音搜索索引。

#### (2) 实时转码与流媒体管道 (`transcode.py`, `stream_target.py`)
- **动态切片**：当客户端不支持直出（如浏览器播放 HEVC/10-bit 或 DTS 音轨）时，异步拉起 FFmpeg 进程，通过管道输出 HLS fMP4 切片（`init.mp4` + `.m4s`）与 `.m3u8`。
- **云端直通转码 (`RemoteInput`)**：FFmpeg 不需要先下载几十 GB 的视频，而是利用 `RemoteInput` 模块将 115 网盘 302 直链按 HTTP Range 分段请求注入 FFmpeg 标准输入，真正做到**零本地硬盘读写**。
- **会话自动回收**：维护全局会话管理器，通过 `_reap_expired` 在客户端暂停或关闭播放器时自动发送信号销毁 FFmpeg 进程。

#### (3) 蓝光原盘与 DVD 零解包直读 (`disc_runtime.py`, `mv-disc-reader`)
- 原镜像内置了 C11 编写的原生工具 `mv-disc-reader`（集成 `libbluray`, `dvdnav`, `dvdread`）。
- **无需解压 50GB ISO**：通过虚拟设备内存映射直接解析 BDMV 目录中的 `mpls` 播放列表与 `clpi` 剪辑信息，定位时长最长的主标题（Main Title），提取音轨、章节与 VobSub 字幕直接送入播放管线。

#### (4) 基于音频指纹的片头片尾跳过 (`intro_detect.py`)
- 使用 FFmpeg 截取剧集前 10 分钟和后 5 分钟的音频，重采样为 16kHz Mono PCM。
- 通过 `numpy.fft.rfft` 与 Hanning 窗函数提取声纹对数功率谱特征。
- 计算同季不同集之间的互相关矩阵（Cross-Correlation），检测相似音频段的重合区间，自动计算出 `intro_start_ticks`、`intro_end_ticks` 并落库，播放器据此呈现“跳过片头”按钮。

#### (5) 弹幕全自动集成 (`danmaku.py`)
- 集成弹弹play（Dandanplay）协议接口。
- 根据影片标题、年份、季集号自动向弹幕服务器搜索并关联 `animeId` 与 `episodeId`，动态下载 XML/JSON 弹幕数据并在前端渲染。

#### (6) Emby 客户端协议全真模拟 (`routers/media_library_emby.py`, `emby.py`)
- 在 `/emby/` 路由下模拟了 Emby 4.9+ 的核心 REST API：
  - `GET /emby/system/info/public`：欺骗 Infuse / VidHub 客户端完成握手。
  - `POST /emby/users/authenticatebyname`：下发专属 Session Token。
  - `GET /emby/users/{user_id}/items`：将数据库实体格式化输出为标准 Emby JSON 报文。
  - `GET /emby/shows/nextup` & `GET /emby/items/resume`：推送继续观看列表。
  - `POST /emby/sessions/playing/...`：双向接收播放进度上报。

---

## 第二部分：新项目设计方案 —— 基于数据库直接构建的虚拟媒体库 (DB-Driven Virtual Media Server)

### 1. 为什么“直接读取数据库”是质的飞跃？

传统方案（如原有方案或挂载本地 STRM）的致命痛点：
1. **庞大的文件系统开销**：当影片数量达到数万部时，成千上万个 `.strm` 文件和海报文件会导致本地磁盘 inode 爆炸，且每次目录扫描（Disk Traverse）耗时极长。
2. **状态脱节与双重维护**：网盘上的文件、本地 STRM 缓存、数据库记录三者容易状态不一致。
3. **FUSE 挂载不稳定**：通过 CloudDrive2 或 AList 挂载的本地目录经常断联，一旦断联，正在运行的媒体库就会判定文件丢失，误删元数据。

**新项目核心思想**：
> **摒弃磁盘作为中间介质，以数据库为唯一真实源（Single Source of Truth）。**
> 媒体库的所有层级结构、元数据、播放地址均**由数据库即时查询投影生成**。当上游入库、下载或转存完成时，直接向数据库写入一行记录，媒体库毫秒级感知，完全无须“全盘扫描”！

---

### 2. 新项目系统架构设计

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                            上游数据生产者                                    │
│   (115/阿里/夸克爬虫/订阅器)  │  (MoviePilot / 自动整理服务)  │  (手动导入/API)  │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ 直接写入 / 更新
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                           核心数据库 (PostgreSQL)                            │
│                                                                             │
│   [media_records]           [cloud_file_nodes]           [user_progress]    │
│   影视元数据与分季结构        网盘原始文件ID/路径/直链      多用户观影时间点   │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                  新项目服务核心 (Virtual Media Engine)                      │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ 1. 动态虚拟目录投影器 (Virtual Library Projector)                      │  │
│  │    • 内存缓存 (Redis/In-Memory Cache) 维护虚拟影视树                   │  │
│  │    • 数据库即时聚合查询 (Grouping by Season/Episode)                  │  │
│  │    • 支持多维度视图：电影、剧集、追番列表、最近新增、年份筛选         │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ 2. 动态直链生成器 (On-Demand Stream Resolver)                          │  │
│  │    • 播放请求进入时，根据 cloud_file_id 实时换取 115 / 阿里直链       │  │
│  │    • 配合防盗链与鉴权下发短时 302 重定向播放 Token                   │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────────────────────────────┐  │
│  │ 3. 轻量级 Emby/Jellyfin 协议模拟器 (Emby Protocol Adapter)           │  │
│  │    • 纯虚拟响应 /emby/Items, /emby/Shows/NextUp 等接口                │  │
│  │    • 让 Apple TV Infuse、VidHub、Kodi 无感直连                        │  │
│  └───────────────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

### 3. 核心模块与实现路径建议

#### 模块一：极简高性能数据表设计

新项目不再需要 `MediaLibrarySource` 复杂的本地文件关联，设计如下 3 张核心表即可：

1. **`virtual_media_items` (影视主表)**
   - `id`: UUID / BigInt
   - `media_type`: `movie` | `series` | `season` | `episode`
   - `parent_id`: 关联上级（如 episode 关联 season，season 关联 series）
   - `title`: 影视标题
   - `title_pinyin`: 拼音首字母（如 `JJDJR`）
   - `season_number` / `episode_number`: 季号、集号
   - `tmdb_id`: TMDB ID（关联演职员、海报等）
   - `poster_url` / `backdrop_url`: 图片可直接引用 TMDB 官方 CDN 或代理地址
   - `overview`: 简介
   - `duration_ms`: 影片时长（毫秒）
   - `intro_markers`: `[start_ms, end_ms]` 片头跳过区间（可选）

2. **`virtual_media_streams` (物理流与网盘映射表)**
   - `item_id`: 对应 `virtual_media_items.id`
   - `cloud_driver`: `115` | `aliyun` | `quark` | `openlist`
   - `cloud_file_id`: 网盘上的真实文件 PickCode / FileID
   - `cloud_file_path`: 网盘内的相对路径
   - `file_size`: 文件体积
   - `video_codec` / `audio_codec` / `resolution`: 码率分辨率信息

3. **`virtual_user_data` (用户播放进度)**
   - `user_id`: 用户 ID
   - `item_id`: 影视条目 ID
   - `playback_position_ms`: 播放断点毫秒
   - `is_played`: 是否已播放完
   - `last_updated_at`: 最后播放时间

---

#### 模块二：即时流地址解析（On-Demand Stream Resolver）

- **痛点解决**：过去 STRM 文件内部写入的是静态 URL，网盘 Cookie 或 Token 过期后 STRM 就会失效。
- **新方案**：
  1. 客户端向服务请求播放：`GET /stream/{item_id}?token=xxx`
  2. 服务从数据库读取该 `item_id` 对应的 `cloud_driver` 和 `cloud_file_id`。
  3. 调用对应网盘 Client（如 115 OpenAPI 或 Cookie Client）实时拉取下载直链。
  4. 直接向客户端响应 **`HTTP 302 Found`**，Location 填入网盘的高速 CDN 链接。
  5. 整个过程耗时 < 100ms，客户端无感知秒开，且**永远不会出现播放链接失效**的问题。

---

#### 模块三：Emby 协议适配器（Emby Adapter）

为了让 Infuse、VidHub、Emby Web 等成熟客户端直接接入，新项目必须对外提供标准 Emby API：

- **握手与信息**：
  - `GET /emby/system/info/public` -> 伪装成 Emby Server 4.9.0。
  - `GET /emby/system/endpoint` -> 返回服务器地址。
- **媒体树与库**：
  - `GET /emby/users/{id}/views` -> 虚拟输出“电影”、“电视剧”两个大分类。
  - `GET /emby/users/{id}/items` -> 支持 `ParentId`、`IncludeItemTypes`、`StartIndex`、`Limit` 等分页查询参数，直接转换为 SQL 的 `SELECT ... FROM virtual_media_items WHERE ... LIMIT 50 OFFSET 0`。
- **会话与进度上报**：
  - `POST /emby/sessions/playing`
  - `POST /emby/sessions/playing/progress`
  - `POST /emby/sessions/playing/stopped`
  接收客户端发来的 `PositionTicks`（转换为毫秒后更新到 `virtual_user_data`）。

---

### 4. 新项目技术选型推荐

| 层次 | 推荐技术栈 | 选型理由 |
| :--- | :--- | :--- |
| **开发语言** | **Go** 或 **Python (FastAPI + Asyncpg)** | 若追求极致吞吐和单二进制交付，推荐 **Go (Gin/Fiber)**；若希望复用已有的 Python 网盘 SDK，推荐 **FastAPI** |
| **数据库** | **PostgreSQL** 或 **SQLite (WAL 模式)** | PostgreSQL 适合多用户并发与海量数据（支持 JSONB 存元数据）；若追求个人单机绿色运行，SQLite 足够支撑 10 万部影视 |
| **缓存层** | **In-Memory Cache (Go-Cache / Redis)** | 缓存热门影视树和分类，使 Infuse 的海报墙滑动帧率拉满 |
| **容器交付** | **Docker Alpine / Scratch** | 无需安装复杂依赖，镜像体积可压缩在 30MB 以内 |

---

### 5. 下一步讨论方向

针对这个新项目，我们可以重点探讨以下几个方向的具体设计：
1. **上游数据源对接**：你计划从哪一个数据库或服务直接导入影视数据？（例如现有的 MediaVault 数据库、MoviePilot 数据库、还是自建的爬虫数据库？）
2. **直链获取机制**：当前重点考虑对接哪些网盘？（115、阿里云盘、天翼云盘、夸克还是 WebDAV/OpenList？）
3. **技术栈定型**：你更倾向于使用 **Go**（单二进制、极致性能、内存低）还是 **Python**（易于快速开发、方便复用现有代码）来构建这个独立项目？
