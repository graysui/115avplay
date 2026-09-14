# Go 极简虚拟媒体服务器架构与设计规范文档
## (Go Virtual Media Server for 115 OpenAPI, Offline DB & JavDB Auto-Enrichment)

---

## 1. 项目愿景与整体架构

本项目旨在打造一个**高性能、单二进制交付、纯数据库驱动**的虚拟媒体服务器。通过将本地电影文件与臃肿的 STRM 文件彻底剥离，系统直接以本地轻量高效的 SQLite 离线数据库（`data/offline_full_merged.db`）作为核心元数据与磁力底座，构建一套**集“离线全量数据融合”、“30天增量每日获取”、“JavDB 榜单定向抓取与磁力补全”、“JavDB 增量刮削与失败超期熔断”、“按需实时磁力搜索与优选”、“115 两级资源调度与 302 秒播”于一体的完整闭环生态**：

```
┌────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                   终端用户与客户端生态 (Emby 协议适配)                                   │
│            Apple TV / Infuse / VidHub / Kodi / 浏览器 Web 播放器 / Android TV 客户端                    │
└───────────────────────────────────────────────────┬────────────────────────────────────────────────────┘
                                                    │ 1. 浏览媒体库 (多大分类)
                                                    │ 2. 搜索番号 (本地检索 / 按需触发 JavDB 在线实时补全)
                                                    │ 3. 发起播放 /videos/:id/stream ➔ 302 重定向直连 115 CDN
                                                    ▼
┌────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                      Go 核心服务器 (Single Binary)                                      │
│                                                                                                        │
│  ┌───────────────────────┐  ┌────────────────────────┐  ┌────────────────────────┐  ┌───────────────┐  │
│  │   虚拟 Emby 模拟层    │  │     JavDB 刮削中心     │  │   按需磁力优选决策器   │  │ Web 管理前端  │  │
│  │  /emby/Items /stream  │  │ 增量刮削/失败10天熔断  │  │ 中字>破解>4K>有码>体积 │  │ Vue3/实时日志 │  │
│  └───────────┬───────────┘  └───────────┬────────────┘  └───────────┬────────────┘  └───────┬───────┘  │
│              │                          │                           │                       │          │
│              └──────────────────────────┼───────────────────────────┴───────────────────────┘          │
│                                         ▼                                                              │
│              ┌──────────────────────────────────────────────────────────────────┐                      │
│              │                 两大数据输入流水线 (互不冲突、去重共存)           │                      │
│              │                                                                  │                      │
│              │  【数据源 1：AVDB-Only 30D增量】    【数据源 2：JavDB 榜单补全】 │                      │
│              │  • 每日凌晨全网论坛增量包          • 用户指定时间拉取周/月/TOP250│                      │
│              │  • AES-256-GCM 解密 ➔ 清洗入库     • 本地未收录 ➔ JavDB搜索入库  │                      │
│              │  • 磁力基于 40位 InfoHash 唯一去重 • 磁力基于 40位 InfoHash 唯一去重 │                  │
│              └──────────────────────────────────┬───────────────────────────────┘                      │
└─────────────────────────────────────────────────┼──────────────────────────────────────────────────────┘
                                                  ▼
                         ┌─────────────────────────────────────────────────┐
                         │       本地 SQLite 3 数据库 (WAL 模式)           │
                         │          data/offline_full_merged.db            │
                         │                                                 │
                         │  • offline_movies  (21.2 万部独立番号与元数据)   │
                         │  • offline_magnets (32.6 万条 40位唯一磁力)     │
                         │  • libraries / user_progress / play_sessions    │
                         └────────────────────────┬────────────────────────┘
                                                  │
                                                  ▼
                         ┌─────────────────────────────────────────────────┐
                         │         115 官方 OpenAPI 资源调度中心           │
                         │                                                 │
                         │  • Tier 1 (已有永久库): 多目录树扫描 ➔ 直链提取 │
                         │  • Tier 2 (临时转存库): 磁力离线秒传 ➔ 到期清理 │
                         └─────────────────────────────────────────────────┘
```

---

## 2. 彻底清理原 MediaVault 历史无用代码与保留资产规范

为了保证新项目架构极致纯粹、零历史包袱，已对工程工作区执行了**深度彻底清理**，移除了全部逆向过程残留的大体积无用镜像与解包文件，累计释放磁盘空间 **1.68 GB**：

### 2.1 已彻底删除的无用文件与目录
1. **`mediavault_rootfs.tar` (499.40 MB)**：原 Docker 容器导出的原始 Linux 根文件系统压缩镜像包（已完全无用，删除）；
2. **`rootfs/` (724.45 MB, 7,477 个文件)**：解包后的完整 Linux 根目录（`/usr`, `/lib`, `/etc`, apt 包等，已无用，删除）；
3. **`mediavault_extracted/` (350.16 MB, 3,521 个文件)**：原 PyInstaller 逆向提取出的 Python 动态链接库（`.so`）、静态资源与第三方依赖包（已无用，删除）；
4. **`app/` (145.58 MB, 259 个文件)**：原 Docker 镜像内置的 Python 遗留代码与 Linux x86_64 ELF 二进制主程序（已无用，删除）；
5. **`scratch/` (3.12 MB)**：逆向初期编写的一次性分析探针与测试脚本；
6. **`magnet-tools/` & `javdb-rankings/`**：内部脚本已全面整合迁移至规范的 `tools/` 工具链中，冗余目录已安全清理。

### 2.2 严格保留的核心资产与职责分工
清理完成后，当前工作区仅保留新项目真正需要的 **4 大核心目录**：

```
mediavault/ (工作区根目录)
├── data/                           # 核心数据持久层
│   ├── offline_full_merged.db      # ★ 核心生产级 SQLite 数据库 (352 MB, 21.2万番号/32.6万磁力)
│   ├── raw_csv/                    # 解密后的全量数据源 (All_sehuatang_full.csv & All_X1080X_full.csv)
│   └── downloads/                  # AVDB 离线全量压缩包存档
├── docs/                           # 权威设计与架构规范
│   ├── PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md # ★ 本系统核心技术设计与架构实现规范
│   └── MEDIA_LIBRARY_SPEC.md       # 原系统技术机制参考剖析
├── references/                     # 逆向提取固化的 8 大核心技术规格 (Markdown/文本/参考代码/SQL)
│   ├── 115_openapi_field_spec.md           # 115 官方 OpenAPI 签名鉴权与核心接口字段规范
│   ├── 115_tree_scan_and_cleanup_spec.md   # 115 目录树递归扫描、番号正则匹配与到期物理清理逻辑
│   ├── emby_protocol_endpoints.md          # 虚拟 Emby 协议必须支持的最小接口清单
│   ├── mediavault_115_client_reference.txt # 115 直链获取并发控制与 SingleFlight 逻辑
│   ├── mediavault_emby_router_reference.txt# 虚拟 Emby 响应载荷格式参考
│   ├── mediavault_media_library_models.py  # 媒体库核心数据结构映射参考
│   ├── mediavault_playback_stream_reference.txt # 播放流调度与 302 重定向起播核心代码
│   └── sakuraplayer_v2_schema.sql          # SakuraPlayer 数据库表结构与索引定义
└── tools/                          # 独立自包含的离线数据与爬虫工具链 (Python 运维脚本)
    ├── avdb_offline_decryptor.py   # AVDB 离线加密 ZIP 包 AES-256-GCM 解密核心模块
    ├── download_full_dbs.py        # 全量数据包自动下载与解密
    ├── merge_offline_dbs.py        # 全量离线库数据清洗、过滤 (彻底删VR/欧美/写真) 与去重入库
    ├── enrich_from_sakura.py       # SakuraPlayer 本地秒级补全工具 (带 2025 年份超期过滤)
    ├── javdb_magnet_fetcher.py     # JavDB 单番号在线磁力与元数据提取器
    └── javdb_rankings_fetcher.py   # JavDB 周榜、月榜、TOP 250 榜单定向抓取器
```

---

## 3. 核心技术栈选型

| 模块 | 选型 | 考量理由 |
|---|---|---|
| **核心语言** | **Go (Golang 1.22+)** | 静态单二进制交付、高并发协程调度、极低内存驻留（常驻约 20~40MB）。 |
| **Web 框架** | **Gin / Echo** | 成熟稳定、路由性能强劲，原生支持 RESTful API、SSE 与 WebSocket。 |
| **数据库** | **SQLite 3 (WAL 模式)** | 单文件免运维部署，挂载 `data/offline_full_merged.db` 毫秒级复合索引查询。 |
| **网盘 SDK** | **自研 115 OpenAPI SDK (Go)** | 专精于 115 官方 OpenAPI，内置 OAuth2 Token 自动续期、多目录扫描与物理删除。 |
| **刮削与爬虫** | **内置 JavDB App API 客户端** | 采用逆向验证的 JavDB v2 API，带动态 User-Agent、防封令牌桶与代理中继。 |
| **前端管理系统** | **Vue 3 + Vite + TailwindCSS + Pinia** | 极简现代化 UI，静态产物通过 Go `embed.FS` 打包，开箱即用。 |

---

## 4. 生产级数据库架构设计 (Database Schema)

数据库以本地已经生成的高质量 `data/offline_full_merged.db` 为基准，核心由 **`offline_movies`**、**`offline_magnets`** 以及服务器运行时辅助表构成：

### 4.1 `offline_movies` 表 (影片番号、元数据与刮削状态)
```sql
CREATE TABLE IF NOT EXISTS offline_movies (
    code                 TEXT PRIMARY KEY,          -- 规范化唯一番号 (如 'SSIS-123', 'FC2-PPV-1234567')
    title                TEXT,                      -- 原始论坛标题 (如 'SSIS-123 【低身長たぬき顔美女】...')
    category             TEXT NOT NULL DEFAULT '亚洲有码',  -- 规范大分类; JavDB 在线来源按 §7.4 推导, 无法判定时回落 '亚洲有码'
    publish_date         TEXT,                      -- 发布/首发日期 (YYYY-MM-DD); 离线论坛源覆盖率 100%, JavDB 在线源可能为 NULL
    preview_images       TEXT,                      -- 逗号分隔的论坛预览图 URL 列表
    source_websites      TEXT,                      -- 来源站点 ('sehuatang', 'x1080x', 'javdb_rankings', 'javdb_search')
    
    -- 元数据增强字段 (已由 SakuraPlayer 本地补全或 JavDB 在线刮削回填)
    title_zh             TEXT,                      -- 中文规范官方翻译标题
    description_zh       TEXT,                      -- 中文剧情简介
    cover_url            TEXT,                      -- JavDB 官方高清海报原图 (横版)
    poster_url           TEXT,                      -- 官方竖版海报图
    actors               TEXT DEFAULT '[]',         -- 演员列表 JSON: [{"name_ja":"乙白さやか","name_zh":"乙白沙耶加"}]
    tags                 TEXT DEFAULT '[]',         -- 题材标签 JSON 数组: ["巨乳", "美少女", "单体作品"]
    maker                TEXT,                      -- 制作片商 (如 'S1 NO.1 STYLE', 'MOODYZ', 'PRESTIGE')
    director             TEXT,                      -- 导演 (如 '大崎広浩治')
    score                REAL DEFAULT 0.0,          -- 评分 (0.00 ~ 5.00)
    
    -- 状态机标记与刮削追踪
    is_enriched          INTEGER DEFAULT 0,         -- 0: 待刮削, 1: 完整刮削成功, 2: 免刮削 (FC2/国产/2025前老片/超期放弃), 3: 部分成功(缺封面或简介) 见 §6.1
    scrape_status        INTEGER DEFAULT 0,         -- 0: 未刮削, 1: 成功, -1: 失败, 2: 放弃
    scrape_failed_reason TEXT,                      -- 刮削失败或放弃的具体原因 (如 'HTTP 404', '超过发布日期10天未收录自动放弃')
    scrape_retry_count   INTEGER DEFAULT 0,         -- 累计重试失败次数
    last_scraped_at      DATETIME,                  -- 最后一次尝试刮削的时间戳
    created_at           DATETIME DEFAULT CURRENT_TIMESTAMP, -- 首次入库时间 (增量对账基准)
    updated_at           DATETIME DEFAULT CURRENT_TIMESTAMP  -- 最近一次字段变更时间 (增量对账基准)
);

CREATE INDEX IF NOT EXISTS idx_off_mov_cat ON offline_movies(category);
CREATE INDEX IF NOT EXISTS idx_off_mov_enriched ON offline_movies(is_enriched);
CREATE INDEX IF NOT EXISTS idx_off_mov_pubdate ON offline_movies(publish_date);
CREATE INDEX IF NOT EXISTS idx_off_mov_scrape ON offline_movies(scrape_status, is_enriched);
```

### 4.2 `offline_magnets` 表 (磁力、多画质多版本与 115 资源生命周期)
```sql
CREATE TABLE IF NOT EXISTS offline_magnets (
    info_hash           TEXT PRIMARY KEY,      -- 40位大写唯一键: btih 取真实 InfoHash; ed2k 取 SHA1(链接); existing 取 'EXIST'+SHA1(pick_code) 前35位, 见 §5.4
    movie_code          TEXT NOT NULL,         -- 关联 offline_movies.code
    magnet_url          TEXT NOT NULL,         -- 完整资源链接: btih 磁力链 / ed2k 链接 / 'existing://<pick_code>' 占位
    resource_kind       TEXT NOT NULL DEFAULT 'btih' CHECK(resource_kind IN ('btih','ed2k','existing')), -- 资源类型, 见 §5.4
    title               TEXT,                  -- 磁力原始标题
    size_mb             INTEGER DEFAULT 0,     -- 文件大小 (MB)
    section             TEXT,                  -- 所属分类
    quality_label       TEXT DEFAULT '1080P',  -- 智能解析画质: '4K', '1080P', '720P'
    website             TEXT,                  -- 采集源站点 ('sehuatang', 'x1080x', 'javdb_search', 'javdb_rankings')
    publish_date        TEXT,                  -- 发布时间
    priority_score      INTEGER DEFAULT 0,     -- 综合优选评分 (计算自: 中字>破解>4K>有码>体积); 0=尚未评分; 评分输入口径见 §7.2
    is_preferred        BOOLEAN DEFAULT 0,     -- 是否为当前番号下的默认优选磁力；由部分唯一索引保证每番号至多 1 条
    
    -- 115 网盘运行时资源绑定
    source_type         TEXT DEFAULT 'temporary' CHECK(source_type IN ('existing','temporary')), -- 'existing'(115已有永久库) 或 'temporary'(临时转存)
    storage_cid         TEXT,                  -- 115 存放目录 CID
    target_file_id      TEXT,                  -- 115 视频文件 file_id
    target_pick_code    TEXT,                  -- 115 提取码 pick_code (换取直链核心)
    target_file_name    TEXT,                  -- 115 内部最大正片文件名
    target_file_size    INTEGER DEFAULT 0,     -- 115 真实文件字节数
    target_container    TEXT DEFAULT 'mp4',    -- 封装格式 (mp4, mkv, ts)
    
    -- 临时转存生命周期管理
    transfer_status     INTEGER DEFAULT 0,     -- 0:未转存, 1:转存中, 2:已就绪, -1:失败, 3:已清理
    is_available        BOOLEAN DEFAULT 0,     -- 资源是否立即可播；权威判定见 §8.0 (仅 transfer_status=2 且 pick_code 非空时为真)
    temp_expire_at      DATETIME,              -- 临时文件到期时间 (永久资源/未转存恒为 NULL)；Janitor 仅处理非 NULL 且已过期项
    last_error          TEXT,                  -- 错误异常日志
    
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(movie_code) REFERENCES offline_movies(code) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_off_mag_code ON offline_magnets(movie_code);
CREATE INDEX IF NOT EXISTS idx_off_mag_pick ON offline_magnets(target_pick_code);
CREATE INDEX IF NOT EXISTS idx_off_mag_status ON offline_magnets(transfer_status, is_available);
CREATE INDEX IF NOT EXISTS idx_off_mag_expire ON offline_magnets(temp_expire_at);
-- 保证每个番号至多一条首选磁力 (SQLite 部分唯一索引)
CREATE UNIQUE INDEX IF NOT EXISTS idx_off_mag_preferred ON offline_magnets(movie_code) WHERE is_preferred = 1;
-- 优选磁力快速查找覆盖索引 (按番号取 preferred，并按评分倒序)
CREATE INDEX IF NOT EXISTS idx_off_mag_preferred_lookup ON offline_magnets(movie_code, is_preferred, priority_score DESC);
```

### 4.3 服务器运行时辅助表 (用户、会话、进度、媒体库分类、系统设置)

> §1 架构图与 §11 工程目录引用了 `libraries / user_progress / play_sessions / settings`，但此前未给出表结构。以下为完整定义，与 `internal/models/` 一一对应。
>
> **会话分层说明**：`auth_sessions` 负责**登录票据**（Emby AccessToken / Web 管理端登录态），`play_sessions` 负责**播放会话**（Emby PlaySessionId，承载进度心跳与暂停状态），二者职责不同、互不替代。

```sql
-- 4.3.1 用户表 (支持多用户；单机部署默认仅 admin 一行)
CREATE TABLE IF NOT EXISTS users (
    id             TEXT PRIMARY KEY,               -- UUID
    username       TEXT NOT NULL UNIQUE,
    password_hash  TEXT NOT NULL,                  -- argon2id 哈希
    is_admin       INTEGER NOT NULL DEFAULT 0,     -- 0/1
    enabled        INTEGER NOT NULL DEFAULT 1,     -- 0/1 停用
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 4.3.2 登录会话 / Emby AccessToken
CREATE TABLE IF NOT EXISTS auth_sessions (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     TEXT NOT NULL,                  -- AccessToken 的 SHA-256，不存明文
    device_name    TEXT DEFAULT '',
    client_name    TEXT DEFAULT '',                -- Infuse / VidHub / Emby Web
    device_id      TEXT DEFAULT '',
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at     DATETIME                        -- NULL 表示永不过期
);
CREATE INDEX IF NOT EXISTS idx_auth_sess_token ON auth_sessions(token_hash);

-- 4.3.3 播放进度与收藏/已看 (每用户每影片一行)
CREATE TABLE IF NOT EXISTS user_progress (
    user_id           TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    movie_code        TEXT NOT NULL REFERENCES offline_movies(code) ON DELETE CASCADE,
    position_ticks    INTEGER NOT NULL DEFAULT 0,  -- Emby 100ns Ticks (1s = 10,000,000)
    duration_ticks    INTEGER DEFAULT 0,
    played            INTEGER NOT NULL DEFAULT 0,  -- 0/1 已看完
    favorite          INTEGER NOT NULL DEFAULT 0,  -- 0/1 收藏 (Emby IsFavorite)
    play_count        INTEGER NOT NULL DEFAULT 0,
    last_played_at    DATETIME,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, movie_code)
);
CREATE INDEX IF NOT EXISTS idx_user_prog_resume ON user_progress(user_id, last_played_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_prog_fav ON user_progress(user_id, favorite);

-- 4.3.4 播放会话 (Emby Session，用于 /sessions/playing* 与并发控制)
CREATE TABLE IF NOT EXISTS play_sessions (
    id              TEXT PRIMARY KEY,              -- PlaySessionId
    user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    movie_code      TEXT NOT NULL,
    media_source_id TEXT,                          -- 选中的 magnet info_hash / MediaSourceId
    device_id       TEXT DEFAULT '',
    position_ticks  INTEGER NOT NULL DEFAULT 0,
    is_paused       INTEGER NOT NULL DEFAULT 0,
    play_method     TEXT DEFAULT 'DirectPlay',
    started_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_play_sess_user ON play_sessions(user_id, updated_at DESC);

-- 4.3.5 媒体库分类 (对应 /emby/library/mediafolders 的 6 大分类)
CREATE TABLE IF NOT EXISTS libraries (
    id             TEXT PRIMARY KEY,               -- 如 'lib_chinese_sub'
    name           TEXT NOT NULL,                  -- 显示名: '中文字幕'
    category       TEXT NOT NULL,                  -- 映射 offline_movies.category
    collection_type TEXT NOT NULL DEFAULT 'movies',-- Emby CollectionType
    sort_order     INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    cover_url      TEXT,
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_lib_category ON libraries(category);

-- 4.3.6 系统设置 (键值对；敏感值加密存储)
CREATE TABLE IF NOT EXISTS system_settings (
    key            TEXT PRIMARY KEY,               -- 如 'temp_transfer_cid', 'cleanup_ttl_days'
    value          TEXT,                           -- 明文值
    is_secret      INTEGER NOT NULL DEFAULT 0,     -- 1 时 value 存密文 (115 Cookie/Token)
    updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 初始化数据 (幂等)
INSERT OR IGNORE INTO libraries (id,name,category,collection_type,sort_order) VALUES
  ('lib_chinese_sub','中文字幕','中文字幕','movies',1),
  ('lib_censored',   '亚洲有码','亚洲有码','movies',2),
  ('lib_uncensored', '亚洲无码','亚洲无码','movies',3),
  ('lib_4k',         '4K原版','4K原版','movies',4),
  ('lib_fc2',        'FC2/素人','FC2/素人','movies',5),
  ('lib_domestic',   '国产','国产','movies',6);

INSERT OR IGNORE INTO system_settings (key,value,is_secret) VALUES
  ('temp_transfer_cid','',0),          -- 115 临时转存目录 CID (Janitor 白名单根)
  ('cleanup_ttl_days','7',0),          -- 临时文件保留天数
  ('scrape_concurrency','2',0),
  ('scrape_delay_min','2.5',0),
  ('scrape_delay_max','4.5',0),
  ('proxy_url','',0),
  ('cleanup_enabled','1',0),
  ('existing_scan_cids','',0),          -- Tier1 已有库扫描根 CID (逗号分隔), 见 §8.3
  ('existing_scan_interval_min','360',0),
  ('transfer_concurrency','2',0),      -- 并发转存任务数上限, 见 §8.4
  ('javdb_search_timeout_ms','6000',0),-- 在线搜索超时, 见 §9.1
  ('stream_wait_ms','8000',0),          -- 播放时同步等待转存的上限毫秒, 见 §8.4
  ('image_proxy','1',0),                -- 1=本地代理缓存图片, 0=302 直链, 见 §9.2
  ('alert_webhook','',0),               -- 告警 Webhook URL, 见 §10.2
  ('log_retention_days','7',0);
```

---

### 4.4 数据库迁移与版本管理

采用 `schema_meta` 表记录 schema 版本，启动时按版本号顺序执行迁移脚本（参考 `references/sakuraplayer_v2_schema.sql` 的 `goose_db_version` 模式）：

```sql
CREATE TABLE IF NOT EXISTS schema_meta (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);
-- 关键键: schema_version (整数，每次结构变更 +1)、db_created_at
```

迁移原则：
1. **只增不删**：新增列用 `ALTER TABLE ADD COLUMN`（带默认值），不做破坏性 DROP；
2. **幂等**：所有 DDL 使用 `IF NOT EXISTS` / `INSERT OR IGNORE`，重启可重复执行；
3. **旧库兼容**：打开既有 `offline_full_merged.db` 时，先补齐缺失列（`offline_movies.created_at`/`updated_at`、`offline_magnets.updated_at`）再补齐缺失表（§4.3），不重建、不丢数据。

---

## 5. 两大数据输入体系的设计与去重共存机制

系统同时具备两大数据输入流水线：**30天增量每日获取** 与 **JavDB 榜单定向抓取**。两者的职责互补，在底层通过 `info_hash` 唯一约束天然融合，**绝不发生数据重复**：

| 对比维度 | 管道一：30天增量每日同步 (Daily 30D) | 管道二：JavDB 榜单定向抓取 (Rankings Ingestion) |
| :--- | :--- | :--- |
| **数据源** | AVDB-Only (Sehuatang + X1080X 论坛发布) | JavDB 官方 App 排行榜接口 (日榜/周榜/月榜/TOP250) |
| **数据特性** | 全网海量、包含大量论坛新旧帖子磁力 | 高口碑、高热度、经全网点赞评分筛选出的精品番号 |
| **触发机制** | 每日凌晨 04:00 定时执行 / 后台一键手动触发 | 用户指定时间周期定时抓取 / 随时按需点击抓取 |
| **处理策略** | 增量解密合并，过滤 VR/欧美/写真，FC2/国产标免补全 | 抓取榜单番号对账；未收录番号调用 JavDB 搜索并优选入库 |
| **磁力去重保障** | **只要 `info_hash` 在 `offline_magnets` 中已存在，自动忽略；不存在则安全入库。** |

### 5.1 管道一：离线库（30天）每日增量获取流程
1. 请求 GitHub Releases API: 探测 `li-peifeng/AVdb-Only` 最新 tag；
2. 流式下载 30 天增量包（`30D_sehuatang` 约 3~5MB + `30D_X1080X` 约 1~2MB）；
3. 内置 AES-256-GCM 密钥内存解密，提取 CSV 数据（PBKDF2-SHA256 派生密钥 + nonce + tag 认证，详见 §5.3）；
4. 数据过滤：丢弃 VR/欧美/写真；FC2/国产置为 `is_enriched = 2`；
5. 对账写入：新番号置为 `is_enriched = 0`，新磁力通过 `info_hash` 插入；
6. 唤醒后台刮削队列。

### 5.2 管道二：JavDB 排行榜抓取与磁力补全流程
复用 `tools/javdb_rankings_fetcher.py` 逆向成功的 App 接口：
- **榜单接口**：`GET /api/v1/rankings`（支持 `period=daily/weekly/monthly`，`type=0全部/1有码/2无码`）；
- **TOP 250 接口**：`GET /api/v1/movies/top`（支持按年份 `year=2024` 或全量 `start_rank=1,51,101...` 分页拉取）。

**榜单号码对账与磁力补全逻辑**：
```
[用户触发抓取周榜/月榜/TOP250]
                │
                ▼
      遍历榜单中的每一部影片番号 (number)
                │
        ┌───────┴───────┐
     [本地已有]      [本地未收录]
        │               │
        │               ▼
        │     调用 JavDB 搜索接口 (GET /api/v2/search?q={number}&type=movie&limit=5)
        │     取精确匹配影片的 movie_id，再分别拉取：
        │       • GET /api/v1/movies/{movie_id}/magnets  官方收录磁力
        │       • GET /api/v1/movies/{movie_id}/reviews  评论区隐藏磁力/ED2K
        │               │
        │               ▼
        │     执行「磁力综合优选评分算法」
        │     (中文字幕 > 破解 > 4K > 有码 > 体积最大 > 唯一磁力)
        │     将最佳磁力标记为 is_preferred = 1
        │               │
        │               ▼
        │     落库 offline_movies (is_enriched = 1；若标题/简介/封面不齐全则为 3)
        │     全量落库 offline_magnets (info_hash 去重)
        │
        ▼
对已有番号进行磁力增量核对 (若有新磁力则追加，不重复则正常插入)
```

### 5.3 离线数据包解密规格 (AES-256-GCM)

> 修正原文档误标的 "AES-CBC"。AVDB 离线包实际采用 **AES-256-GCM**（带认证标签），CBC 与 GCM 不兼容，按 CBC 实现将无法解密。

外层 ZIP 包内包含两份文件：清单 `avdb-resource-library.json` 与密文载荷 `*.bin`。解密步骤：

| 步骤 | 操作 | 参数/说明 |
| :--- | :--- | :--- |
| 1 | 读取清单 | 从外层 ZIP 读取 `avdb-resource-library.json` |
| 2 | 解析字段 | `payload`(载荷名), `salt`(base64), `nonce`(base64, 12B), `tag`(base64, 16B), `iterations`(默认 200000) |
| 3 | 密钥派生 | `key = PBKDF2-HMAC-SHA256(password_digest, salt, iterations, dklen=32)` |
| 4 | 认证解密 | `plaintext = AES-256-GCM_Decrypt(key, nonce, ciphertext || tag)` |
| 5 | 解开内层 ZIP | 上述 `plaintext` 是一个内层 ZIP，其中含 CSV 数据文件 |

- `password_digest`：32 字节固定摘要（供 PBKDF2 作口令输入），实现上作为常量内置；
- **Go 实现依赖**：`golang.org/x/crypto/pbkdf2` + 标准库 `crypto/aes` + `crypto/cipher`(`NewGCM`)；
- **失败处理**：GCM 认证失败（tag 不匹配）必须直接报错拒绝，不得回退试 CBC，避免掩盖密钥/载荷损坏问题。

### 5.4 资源类型与唯一键 (`resource_kind`)

`offline_magnets.info_hash` 为 40 位十六进制唯一键，但不同资源类型来源不同，需用 `resource_kind` 区分。三条链路的归一化规则与转存接口：

| resource_kind | `info_hash` 生成规则 | `magnet_url` 内容 | 115 转存接口 |
| :--- | :--- | :--- | :--- |
| `btih` (默认) | 磁力链中 `urn:btih:` 的 40 位 InfoHash（大写） | 完整磁力链 | `/open/offline/add_task_bt` |
| `ed2k` | `SHA1(规范化 ed2k 链接).upper()`（40 位） | 完整 ed2k 链接 | `/open/offline/add_task_urls` |
| `existing` | `'EXIST' + SHA1(pick_code).upper()[:35]`（共 40 位） | `existing://<pick_code>` 占位 | 无需转存（已在库，见 §8.3） |

要点：
1. **ed2k 不能丢弃**：JavDB 评论区大量分享为 ed2k，没有 btih。用其链接的 SHA1 作为合成键即可无缝纳入同一去重体系；
2. **规范化**：ed2k 先去掉首尾空白与尾部标点再算 SHA1，保证同一链接幂等；
3. **解析器分发**：入库时先尝试 `urn:btih:`，命中则 `btih`；否则识别 `ed2k://` 前缀置 `ed2k`；均不命中则丢弃并计入 `dropped_invalid`；
4. **转存分发**：`transfer_worker` 按 `resource_kind` 选择上表对应接口，`existing` 直接跳过转存；
5. **前端展示**：`resource_kind` 不对外暴露，统一按“磁力/版本”展示。

---

## 6. JavDB 刮削状态追踪、失败记录与超期熔断机制

### 6.1 刮削生命周期与状态机
在刮削过程中，系统不仅回填元数据，还会对每一次网络请求的结果进行审计记录：

```
                    待刮削/待补全记录 (is_enriched IN (0,3))
                               │
                               ▼
                      调用 JavDB 移动端 API
                               │
             ┌─────────────────┴─────────────────┐
          [请求成功]                           [请求失败]
             │                                   │
             ▼                                   ▼
   回填中文标题/简介/封面/演员          记录 scrape_failed_reason
             │                            scrape_retry_count += 1
             ▼                            scrape_status = -1
   【关键字段完整性校验】                  last_scraped_at = NOW
   标题 + 简介 + 封面是否齐全?                    │
             │                                    ▼
      ┌──────┴──────┐               【检查 10 天超期熔断规则】
   [齐全]        [缺封面/简介]       当前日期 - publish_date > 10 天?
      │              │                          │
      ▼              ▼                ┌─────────┴─────────┐
 is_enriched=1   is_enriched=3      [是]                 [否]
 scrape_status=1 (部分成功,待补全)    │                   │
 last_scraped_at scrape_status=1     ▼                   ▼
   =NOW          scrape_failed_  标记为免刮削/放弃刮削  保留 is_enriched=0
                 reason='缺封面'   is_enriched=2        待下个批次延时重试
                 |'缺简介'         scrape_status=2
                 → 仍参与补全批次   记录: '超期10天未收录'
```

> **状态取值说明**：`is_enriched` 取 `0/1/2/3` —— `0` 待刮削；`1` 完整成功（标题+简介+封面齐全）；`2` 免刮削（FC2/国产/超期放弃）；`3` 部分成功（请求成功但缺封面或简介）。**补全批次的筛选条件为 `is_enriched IN (0, 3)`**，避免部分成功记录被永久冻结；前端“待刮削”计数与 `idx_off_mov_enriched` 查询需同时统计 `0` 与 `3`。

### 6.2 核心规则：失败资源超过资源日期 10 天自动放弃刮削
- **业务依据**：新片在论坛发布后，通常在 1~3 天内就会被 JavDB 官方数据库收录。若一个资源发布日期距离当前已经超过 **10 天**，且多次刮削仍返回 404 或无数据，则该资源绝大多数属于“论坛自制合集”、“生僻乱码编号”或“已被下架废番”；
- **自动熔断判定**：
  ```go
  // publish_date 可能为 NULL (JavDB 在线来源)，此时跳过“超期”判定，仅靠重试次数熔断
  giveUpByAge := publishDate != nil && time.Since(*publishDate).Hours()/24 > 10
  giveUpByRetry := retryCount >= 3
  if (giveUpByAge || giveUpByRetry) && (errIs404 || giveUpByRetry) {
      movie.IsEnriched = 2
      movie.ScrapeStatus = 2
      if giveUpByAge {
          movie.ScrapeFailedReason = fmt.Sprintf("发布已超 %d 天仍未收录，自动放弃刮削", int(time.Since(*publishDate).Hours()/24))
      } else {
          movie.ScrapeFailedReason = fmt.Sprintf("连续 %d 次抓取失败，自动放弃刮削", retryCount)
      }
  }
  ```

---

## 7. 番号未命中时 JavDB 在线磁力实时搜索与优选决策引擎

### 7.1 业务触发场景
用户在 Infuse / VidHub 客户端或 Web 管理端搜索某个番号，若本地数据库 `offline_movies` 中**无此番号**：
1. 本地未命中，系统无缝接管，调用 JavDB 在线实时搜索；
2. 动态抓取该番号详情页中的**全量可用磁力链接列表**与官方元数据；
3. 执行资源综合优选决策，遴选首选播放磁力；
4. 自动落库自愈，立即返回给客户端。

### 7.2 磁力优选决策链（严格遵循用户权重排序）
系统对候选磁力执行多级优先级加权评分：
$$\text{中文字幕} > \text{破解 (无码流出)} > \text{4K 高清} > \text{标准有码} > \text{体积最大} > \text{唯一磁力保底}$$

> **评分输入口径（唯一权威）**：一律读取 `offline_magnets.title`（磁力原始标题）做关键字匹配，函数签名为 `CalculatePriorityScore(title, sizeMB, hasOnlyOne)`。`quality_label` **仅用于展示与筛选，不参与评分**，避免“标题写 4K 但 label 判为 1080P”导致的输入歧义。
>
> **评分触发点（写回 `priority_score`）**：磁力入库时（§5.1 增量合并）、榜单补全时（§5.2）、以及番号按需在线搜索落库时（§7.1）三处统一调用，并在同一事务内按 §7.3 重选 `is_preferred`。

#### 决策评分算法实现：
```go
func CalculatePriorityScore(title string, sizeMB int, hasOnlyOne bool) int {
    if hasOnlyOne {
        return 999999 // 唯一磁力保底入选
    }
    
    score := 0
    t := strings.ToUpper(title)
    
    // 1. 最高优先级：中文字幕 (+100,000)
    if strings.Contains(t, "中文字幕") || strings.Contains(t, "-C") || 
       strings.Contains(t, "_C") || strings.Contains(t, "中字") || 
       strings.Contains(t, "字幕") {
        score += 100000
    }
    
    // 2. 次高优先级：无码破解 / 流出 (+50,000)
    if strings.Contains(t, "破解") || strings.Contains(t, "流出") || 
       strings.Contains(t, "UNCENSORED") || strings.Contains(t, "LEAKED") {
        score += 50000
    }
    
    // 3. 第三优先级：4K / 2160P 超清规格 (+25,000)
    if strings.Contains(t, "4K") || strings.Contains(t, "2160P") || 
       strings.Contains(t, "UHD") {
        score += 25000
    } else if strings.Contains(t, "1080P") || strings.Contains(t, "FHD") || 
              strings.Contains(t, "蓝光") || strings.Contains(t, "BLURAY") {
        // 4. 第四优先级：标准有码 1080P (+10,000)
        score += 10000
    } else {
        score += 5000 // 720P / 普通标清
    }
    
    // 5. 第五优先级：体积权重 (在同等规格下，体积越大通常码率画质越高)
    sizeBonus := (sizeMB / 1024) * 100
    if sizeBonus > 5000 {
        sizeBonus = 5000
    }
    score += sizeBonus
    
    return score
}
```

---

### 7.3 首选磁力重选机制 (`is_preferred`)

`offline_magnets` 通过部分唯一索引 `idx_off_mag_preferred` 保证**每个番号至多一条 `is_preferred = 1`**。重选必须在**同一事务**内执行，避免中间态违反唯一约束：

1. `UPDATE offline_magnets SET is_preferred = 0 WHERE movie_code = ? AND is_preferred = 1;`
2. 向上取候选：优先选“可立即播放”的（§8.0 可播条件成立），否则回落至全部未失效磁力；
3. `ORDER BY priority_score DESC, size_mb DESC, info_hash ASC LIMIT 1` 取第一条置 `is_preferred = 1`；
4. 唯一的磁力`hasOnlyOne` 情况由 §7.2 直接返回 `999999` 保底入选。

**触发时机**（均自动重选）：磁力新增、`priority_score` 变更、`transfer_status`/可播性变更、磁力被清理失效。若某番号所有磁力均失效，则保留最高分者为 preferred，以便下一次播放重建时直接触发转存。

### 7.4 JavDB 在线搜索落库的字段推导规则

§7.1 的在线自愈会向 `offline_movies` 插入新记录，但 JavDB 并不直接返回本系统的规范分类与完整字段，需按下表推导（解决 `category`/`publish_date` 的 NOT NULL/缺失冲突）：

| 目标字段 | 推导规则 |
| :--- | :--- |
| `code` | JavDB `number` 经 §2 归一化（FC2 统一为 `FC2-PPV-xxxxxxx`）|
| `title` | `number + ' ' + JavDB原始标题` |
| `category` | 优先看标题关键词（`中文字幕`→中文字幕；`4K/2160P`→4K原版；`无码/UNCENSORED`→亚洲无码）；否则由 JavDB `video_type` 映射（`censored`→亚洲有码、`uncensored`→亚洲无码、`fc2`→FC2/素人）；**仍无法判定时回落 '亚洲有码'** |
| `publish_date` | 取 JavDB `release_date`；缺失则写入 **NULL**（不阻断入库）|
| `title_zh`/`description_zh`/`cover_url`/`poster_url`/`actors`/`tags`/`maker`/`director`/`score` | 直接回填 JavDB 字段 |
| `source_websites` | 追加 `javdb_search`（逗号分隔，已存在则不重复）|
| `is_enriched` | 标题+简介+封面齐全置 `1`，否则置 `3`（与 §6.1 一致）|
| `scrape_status` | 置 `1`（请求成功），仅当请求本身失败才置 `-1` |

**拒绝入库规则**（与 §5 摄入过滤口径一致）：若 JavDB 结果被判定为 VR / 欧美 / 写真，则**不落库**，对该次客户端搜索返回“无结果”，避免污染媒体库。

> 原子性：先 upsert `offline_movies`（`ON CONFLICT(code) DO UPDATE`，仅填补空字段，不覆盖已有离线元数据），再全量 upsert `offline_magnets`（按 `info_hash` 去重），最后在同一事务内计算 `priority_score` 并按 §7.3 重选 `is_preferred`。

---

## 8. 115 资源调度、两级播放管线与生命周期清理

### 8.0 可播性权威判定 (Source of Truth)

`transfer_status` 与 `is_available` 语义存在重叠，为避免查询口径分裂，规定如下：

| 判定优先级 | 字段 | 说明 |
| :--- | :--- | :--- |
| **权威** | `transfer_status` + `target_pick_code` | 决定资源真实状态 |
| 派生 | `is_available` | 由权威字段推导的冗余标记，仅供索引快速过滤 |

**可播条件（必须同时满足）**：
1. `target_pick_code` 非空；
2. 且满足以下任一：
   - `source_type = 'existing'`（115 已有永久库，无过期概念）；或
   - `transfer_status = 2`（转存就绪）且 (`temp_expire_at IS NULL OR temp_expire_at > NOW`)；
3. `is_available` 必须与上述推导一致，任何写库路径都不得单独修改 `is_available`。

> Janitor 清理、目录同步回填、转存完成回填等所有路径，统一调用 `recomputeAvailability(magnet)` 重算，杜绝字段漂移。

### 8.1 两级调度播放管线
1. **Tier 1 (已有永久库)**：优先比对 115 已配置影视目录树中的视频（`source_type = 'existing'`），命中直接换取 115 CDN 直链，**HTTP 302 Found** 秒播；
2. **Tier 2 (临时转存库)**：已有库未命中时，自动调用 115 OpenAPI 对选定的 `is_preferred` 优选磁力发起离线秒传；提取视频直链并打上 `temp_expire_at` 到期时间。

### 8.2 临时文件定时物理清理 (Janitor)
1. **保留时长 TTL**：由 `system_settings.cleanup_ttl_days` 配置，**默认 7 天**（转存就绪时写入 `temp_expire_at = ready_at + TTL`）；`cleanup_enabled = 0` 时暂停清理。仅处理 `temp_expire_at IS NOT NULL AND temp_expire_at <= NOW` 的记录，永久资源（NULL）永不被清理；
2. **白名单目录防误删**：严格限制物理删除仅能在 `system_settings.temp_transfer_cid` 指定的临时目录之下执行，严禁触碰已有影视目录；
3. **OpenAPI 物理删除**：调用 `POST https://proapi.115.com/open/ufile/delete` 释放 115 云盘空间；删除为异步操作，需间隔 1~2s 并校验后续返回值；
4. **数据库联动对账**：将已删除磁力的 `target_pick_code` 与 `target_file_id` 置空，`transfer_status` 重置为 `0`（未转存），并经 §8.0 重算 `is_available = 0`，彻底杜绝失效死链。

### 8.3 Tier1 已有库扫描与 `source_type='existing'` 写入契约

补充 §8.1 所述 Tier1 的落地规则（解决“`source_type='existing'` 由谁写入”的空档）：

- **配置**：`existing_scan_cids`（逗号分隔根 CID）、`existing_scan_interval_min`（默认 360）、`transfer_concurrency`；
- **Worker** `services/tree_scanner.go` 每轮流程：
  1. 对每个根 CID 用 `GET /open/ufile/files?cid=&show_dir=1` 列出子目录；
  2. 用 `115_tree_scan_and_cleanup_spec.md` §2 的番号正则从目录名提取并归一化 `code`；
  3. 与 `offline_movies.code` 匹配；未命中则记日志跳过（不新建影片）；
  4. 目录内用 `type=4` 列出视频，剔除 sample/预告片/`<100MB`，取**最大文件**为主片；
  5. **Upsert `offline_magnets`**：`resource_kind='existing'`、`source_type='existing'`、`info_hash='EXIST'+SHA1(pick_code)[:35]`、`magnet_url='existing://<pick_code>'`、`storage_cid`、`target_file_id/name/size/container/pick_code`、`priority_score=900000`（**高于任何磁力，确保 Tier1 永远优先**）、`is_available` 经 §8.0 置 `1`、`temp_expire_at=NULL`、`last_error=NULL`；
  6. 同一 `code` 单飞；事务内按 §7.3 重选 preferred。
- **幂等**：重复扫描更新同一行，不产生重复记录；
- **失效处理**：若目录/文件在 115 已不存在，清空 pick_code/file_id、`transfer_status=0`，经 §8.0 置 `is_available=0`，交由 §8.4 回退转存。

### 8.4 播放取链失败与转存中降级 (Fallback)

`GET /emby/videos/{id}/stream` 的完整决策链（解决死链回退未闭环问题）：

1. 解析 `ItemId` + `MediaSourceId` → 定位 `offline_magnets` 行（未传 `MediaSourceId` 则取 preferred）；
2. **可播（§8.0 成立）** → 调 `/open/ufile/downurl` 取直链 → `302`；
3. 若 `downurl` 失败（file not found / errno≠0）→ 立即经 §8.0 置 `is_available=0`、清 pick_code，转入步骤 4（**不返回破碎 302**）；
4. **不可播时**：
   - `resource_kind='existing'` 但 pick_code 失效 → 触发该目录重扫（§8.3）；
   - 否则入队转存任务（按 `movie_code` 单飞，并发上限 `transfer_concurrency`）；
   - **同步等待**至多 `stream_wait_ms`（默认 8000）：若转存就绪 → 取链 `302` 真流；
   - **超时** → `302` 到内置占位短片 `/static/preparing.mp4`（约 5s 循环）并回 `X-MV-Preparing: 1`，客户端提示“正在准备，请稍后重试”，重试即得真流；
5. 转存进度通过 WebSocket 推送到管理端；播放端靠“重试请求”自然获得新状态；
6. **Single-flight**：同一 `movie_code` 的并发转存请求合并为一个任务，避免重复占用 115 离线配额。

---

## 9. 虚拟 Emby 协议模拟与多版本聚合

系统对外完整模拟 Emby 4.8+ 核心协议，使 Apple TV Infuse、VidHub、Kodi 可无感接入：
- `GET /emby/library/mediafolders`：直接输出大分类（中文字幕、亚洲有码、亚洲无码、4K原版、FC2/素人、国产）；
- `GET /emby/users/{uid}/items?SearchTerm={code}`：**全局搜索仅返回 1 张干净的海报卡片**；
- `GET /emby/users/{uid}/items/{id}`：在详情页下发 `MediaSources` 数组，展示中字、破解、4K、1080P 等多版本与文件体积，支持客户端自由切换；
- `GET /emby/videos/{id}/stream`：核心播放接口，通过 HTTP 302 重定向到 115 CDN 直连。

### 9.1 搜索命中与降级边界

- **本地命中**：`WHERE code LIKE ? OR title LIKE ? OR title_zh LIKE ?`，毫秒返回；
- **本地未命中** → 调 JavDB 在线搜索（落库规则见 §7.4）：
  - 超时 `javdb_search_timeout_ms` 默认 6000；
  - 按 `code` 做 **single-flight**，并发搜同一番号只发一次请求；
  - 成功 → 事务落库后返回 1 张卡片；
  - 失败/风控/超时 → **返回空列表（HTTP 200, `Items: []`）**，不返回 5xx，避免客户端弹错；后台异步重试 1 次；
- 搜索结果为**同步落库**（事务提交后再返回），保证下一次请求可直接命中本地。

### 9.2 图片服务

- **路由**：`GET /emby/items/{id}/images/{type}`（`Primary` / `Backdrop` / `Logo`）；
- **字段映射**：`Primary`→`poster_url`（竖版）优先，回落 `cover_url`；`Backdrop`→`cover_url`（横版）优先，回落 `poster_url`；均无→内置占位图；
- **取图模式**（由 `image_proxy` 控制）：
  - `1`（默认，本地代理缓存）：服务端拉取 → 磁盘缓存 `data/image_cache/<sha256>.<ext>` → 回 `Cache-Control: public, max-age=86400` + `ETag`，二次请求命中缓存，避免源站盗链/失效；
  - `0`：`302` 到源 URL；
- **容错**：源站 404/超时 → 返回占位图 + `Cache-Control: no-store`，**绝不 5xx**；
- Item DTO 的 `ImageTags.Primary/Backdrop` 必须填对应 hash，客户端才会发起图片请求。

### 9.3 鉴权与会话

- **首次启动**：`users` 为空 → 创建 `admin`，密码取环境变量 `MV_ADMIN_PASSWORD`，未设则随机生成并打印到日志（仅一次）；
- **管理端**：`POST /api/admin/login`（argon2id 校验）→ 建 `auth_sessions` → 下发 HttpOnly Cookie / Bearer；
- **Emby 客户端**：`POST /emby/users/authenticatebyname` → 校验 `users` → 建 `auth_sessions` → 返回随机 32 字节 `AccessToken`（DB 仅存 SHA-256）；
- **中间件**：除 `/emby/system/info/public` 与 `/emby/users/authenticatebyname` 外，`/emby/*` 均需 `X-Emby-Token` / `api_key` 命中 `auth_sessions`；
- **用户信息**：`GET /emby/users/me`、`GET /emby/users/{id}` 返回 user DTO（含 `Policy.IsAdministrator` 等）；
- **多用户**：`users` 表支持多行；单机部署默认仅 admin。

### 9.4 进度、收藏与已看

- `POST /emby/sessions/playing` → upsert `play_sessions` + `user_progress.position_ticks`；
- `POST /emby/sessions/playing/progress` → 更新 position（`PositionTicks`，1s = 10,000,000）；
- `POST /emby/sessions/playing/stopped` → 更新；若 `position/duration >= 0.9` 置 `played=1` 并 `play_count+1`；
- **收藏**：`POST/DELETE /emby/users/{uid}/favoriteitems/{id}` → `user_progress.favorite`；
- **已看**：`POST/DELETE /emby/users/{uid}/playeditems/{id}` → `user_progress.played`；
- **继续观看**：`GET /emby/users/{uid}/items/resume` → `played=0 AND position_ticks>0 ORDER BY last_played_at DESC`；
- 所有写入按 `(user_id, movie_code)` UPSERT，保证多用户隔离。

---

## 10. 前端管理控制台 (Admin Web UI)

一体化 Web 控制台基于 Vue 3 + Vite + TailwindCSS 构建，通过 `go:embed` 打包进单二进制：

```
┌────────────────────────────────────────────────────────────────────────────────────────────────┐
│  GoMediaVault Admin                                                           ● 115已连接 [退出]│
├───────────┬────────────────────────────────────────────────────────────────────────────────────┤
│ 仪表概览   │ 【刮削任务中心】                                                                    │
│ 媒体库分类 │  待刮削(0+3): N 部 | 完整(1): N 部 | 部分(3): N 部 | 免刮削(2): N 部              │
│ 番号目录   │                                                                                    │
│ 刮削中心   │  [⚡ 全量刮削无元数据资源] --------------------------------------------------------│
│ 榜单抓取   │  可选资源日期范围: [ 2025-01-01 ] 至 [ 2026-09-14 ]   [✔ 包含已失败]  [开始执行]    │
│ 30天增量   │  并发协程数: [ 2 ] | 请求随机延时: [ 2.5 ~ 4.5 秒 ] | 代理配置: [ 127.0.0.1:7890 ]  │
│ 实时日志   │                                                                                    │
│ 系统设置   │ 【JavDB 榜单定向获取】                                                             │
│           │  [抓取周榜] [抓取月榜] [抓取年度TOP250] ➔ 自动比对本地并补全磁力库                 │
├───────────┴────────────────────────────────────────────────────────────────────────────────────┤
│ [实时日志流 (WebSocket)]                                                               [清空] [⏸]│
│ 16:30:01 [INFO]  [Sync-30D]  获取 GitHub 30D 增量包: 解密合并 124 条新磁力 (无冲突入库)        │
│ 16:30:05 [INFO]  [Rankings]  抓取 JavDB 本周有码周榜 (50部): 发现本地未收录番号 SSIS-999        │
│ 16:30:06 [INFO]  [Priority]  为 SSIS-999 优选磁力: '[中字] 4K 压制' (得分: 125800) 自动落库   │
│ 16:30:10 [WARN]  [Scraper]   番号 MIAA-001 刮削失败: HTTP 404 (重试 1 次)                      │
│ 16:30:12 [WARN]  [Scraper]   番号 OLD-123 刮削失败，检测到发布于 2024-05-01 (>10天)，熔断为放弃 │
│ 16:30:15 [INFO]  [Stream]    客户端请求 IPX-123, 命中已有永久资源 ➔ 302 直连 115 CDN           │
└────────────────────────────────────────────────────────────────────────────────────────────────┘
```

### 10.1 管理端 REST API 清单

将 §10 UI 的每个按钮/面板映射到具体接口（补充此前“有按钮无接口定义”的缺口）：

| 方法 | 路径 | 说明 / 关键参数 |
| :--- | :--- | :--- |
| POST | `/api/admin/login` | `{username, password}` → Set-Cookie |
| POST | `/api/admin/logout` | 销毁当前 `auth_sessions` |
| GET | `/api/stats` | `{pending, enriched, skipped, partial, total}`（pending = `is_enriched IN (0,3)`）|
| GET | `/api/config` | 返回 `system_settings`（`is_secret=1` 的字段脱敏）|
| PUT | `/api/config` | 批量更新设置（115 目录、TTL、并发、代理等）|
| GET | `/api/movies` | `?q=&category=&is_enriched=&page=&page_size=` 分页 |
| GET | `/api/movies/{code}` | 详情 + 磁力列表（含优选项）|
| PUT | `/api/movies/{code}` | 手动修正元数据 |
| DELETE | `/api/movies/{code}` | 删除影片及其磁力 |
| POST | `/api/scraper/run` | `{date_from, date_to, include_failed, concurrency, delay_min, delay_max, proxy}` |
| GET | `/api/scraper/status` | 进度 / 命中率 / 熔断数 |
| POST | `/api/rankings/run` | `{board: daily\|weekly\|monthly\|top250, year?}` |
| POST | `/api/sync30d/run` | 手动触发 30 天增量同步 |
| GET | `/api/tasks/{id}` | 异步任务状态 |
| GET | `/api/logs/stream` | WebSocket 实时日志流 |
| GET | `/healthz`、`/readyz` | 存活 / 就绪探针 |

### 10.2 日志、健康检查与告警

- **日志中枢** `services/log_hub.go`：内存 ring buffer（默认 2000 条）+ 可选文件落盘；级别 `DEBUG/INFO/WARN/ERROR`；保留时长 `log_retention_days`；
- **实时流**：`GET /api/logs/stream`（WebSocket），前端“实时日志”面板订阅；
- **健康检查**：`/healthz` 仅校验进程存活；`/readyz` 额外校验 DB 可读写 + 115 token 有效期 + 最近一次 30D 同步时间；
- **告警触发点**：115 token/refresh 失败、JavDB 风控（429/403）、转存连续失败、磁盘空间不足、30D 同步失败；
- **投递方式**：写 `ERROR` 日志 + 前端顶部横幅 + `alert_webhook`（已配置时 POST JSON）；同一告警按 key 去重并设静默窗口（默认 1h）。

---

## 11. 后端 Go 工程完整目录结构规范

```
mediavault/ (工作区根目录)
├── cmd/
│   └── server/
│       └── main.go                 # 主程序入口，初始化各子系统并启动 HTTP/WebSocket
├── internal/
│   ├── api/                        # Web 后台管理 REST 路由
│   │   ├── admin_auth.go           # 管理员登录与鉴权
│   │   ├── config.go               # 115 目录、清理 TTL、刮削速率等配置管理
│   │   ├── stats.go                # /api/stats 统计聚合 (待刮削 0+3 / 完整 1 / 部分 3 / 免刮削 2)
│   │   ├── health.go               # /healthz, /readyz 探针
│   │   ├── movies.go               # 番号与磁力管理 CRUD
│   │   ├── scraper_ctrl.go         # 刮削任务控制接口 (全量刮削/日期范围/进度上报)
│   │   ├── rankings_ctrl.go        # JavDB 榜单手动/定时抓取接口
│   │   ├── sync_30d.go             # 30天增量手动/定时同步触发接口
│   │   └── ws_logs.go              # WebSocket 实时日志流推送广播
│   ├── emby/                       # 虚拟 Emby 4.8+ 协议模拟层
│   │   ├── router.go               # 注册 /emby/... 路由
│   │   ├── handshake.go            # /system/info, /users/authenticatebyname
│   │   ├── library.go              # /library/mediafolders, /items (支持按需搜索扩展)
│   │   ├── search.go               # 搜索命中与 JavDB 在线降级 (见 §9.1)
│   │   ├── images.go               # /items/{id}/images/* 代理缓存与占位图 (见 §9.2)
│   │   ├── middleware.go           # AccessToken 校验与用户上下文 (见 §9.3)
│   │   ├── playback.go             # /videos/:id/stream (两级调度 302 + §8.4 降级)
│   │   └── session.go              # 播放进度心跳与断点持久化
│   ├── client115/                  # 115 官方 OpenAPI SDK 封装
│   │   ├── client.go               # 统一 HTTP 请求与签名
│   │   ├── oauth.go                # OAuth2 Token 刷新与自动续期
│   │   ├── offline.go              # 离线添加磁力与转存状态轮询
│   │   ├── tree_scan.go            # 目录树拉取与最大视频智能探测
│   │   ├── delete.go               # 物理删除临时文件 (POST /open/ufile/delete)
│   │   └── downurl.go              # 提取视频直链与并发单飞缓存
│   ├── javdb/                      # JavDB 综合刮削、搜索与榜单引擎
│   │   ├── client.go               # JavDB 移动端 API 基础封装 (签名/Header/代理)
│   │   ├── scraper.go              # 后台温和刮削 Worker (含 10 天超期熔断规则)
│   │   ├── rankings.go             # 周榜/月榜/TOP250 抓取与磁力补全引擎
│   │   ├── search.go               # 番号未命中时在线实时搜索磁力与元数据
│   │   ├── ingest.go               # JavDB 结果→offline_movies/magnets 字段推导落库 (§7.4)
│   │   ├── priority.go             # 磁力资源优选决策器 (中字>破解>4K>有码>体积)
│   │   └── ratelimit.go            # 令牌桶限流与随机延时控制器
│   ├── ingestion/                  # 离线数据包每日同步引擎
│   │   ├── daily_30d.go            # GitHub Release 30天增量包探测与下载
│   │   ├── decryptor.go            # AES-256-GCM 内存级解密器 (PBKDF2 派生密钥)
│   │   └── merger.go               # 增量去重、分类过滤与 SQLite 合并
│   ├── db/                         # 数据库持久层
│   │   ├── database.go             # SQLite 连接池配置 (挂载 offline_full_merged.db)
│   │   ├── migrate.go              # schema_meta 版本迁移与旧库补列/补表 (见 §4.4)
│   │   └── queries.go              # 高频核心 SQL 封装
│   ├── models/                     # 实体数据结构 (对应 §4.3 表结构)
│   │   ├── movie.go                # OfflineMovie (offline_movies)
│   │   ├── magnet.go               # OfflineMagnet (offline_magnets)
│   │   ├── user.go                 # User (users)
│   │   ├── auth_session.go         # AuthSession (auth_sessions, 登录票据)
│   │   ├── play_session.go         # PlaySession (play_sessions, 播放会话)
│   │   ├── progress.go             # UserProgress (user_progress)
│   │   ├── library.go              # Library (libraries)
│   │   └── setting.go              # SystemSetting (system_settings)
│   └── services/                   # 后台常驻 Worker 服务
│       ├── tree_scanner.go         # 115 已有影视目录定时扫描与番号识别 Worker (Tier1 写入, §8.3)
│       ├── transfer_worker.go      # 离线秒传与最大视频探测 Worker (含 resource_kind 分发)
│       ├── janitor.go              # 临时转存到期物理删除与数据库状态联动 Worker
│       ├── availability.go         # recomputeAvailability() 可播性统一重算 (§8.0)
│       ├── image_cache.go          # 海报磁盘缓存与去重下载 (§9.2)
│       ├── alert.go                # 告警去重、静默窗口与 Webhook 投递 (§10.2)
│       ├── job_registry.go         # 异步任务注册与查询 (/api/tasks/{id})
│       └── log_hub.go              # 统一内存 Pub/Sub 日志中继中心
├── web/                            # 前端单页应用 (Vue 3 + TailwindCSS)
│   ├── src/
│   │   ├── views/                  # Dashboard, Movies, Scraper, Rankings, Settings, Logs
│   │   └── components/
│   ├── dist/                       # Vite 编译静态文件目录
│   └── embed.go                    # //go:embed dist/* 静态资源嵌入
├── data/                           # 核心数据持久化挂载目录
│   ├── offline_full_merged.db      # 核心生产离线数据库 (352 MB)
│   ├── raw_csv/                    # 解密全量数据表
│   └── downloads/                  # 增量包缓存目录
├── references/                     # 逆向固化的 8 大核心技术规格文档库
│   ├── 115_openapi_field_spec.md
│   ├── 115_tree_scan_and_cleanup_spec.md
│   ├── emby_protocol_endpoints.md
│   ├── mediavault_115_client_reference.txt
│   ├── mediavault_emby_router_reference.txt
│   ├── mediavault_media_library_models.py
│   ├── mediavault_playback_stream_reference.txt
│   └── sakuraplayer_v2_schema.sql
├── tools/                          # 独立离线数据工具链 (Python 运维脚本)
│   ├── avdb_offline_decryptor.py   # AVDB AES-256-GCM 解密核心库
│   ├── download_full_dbs.py        # 全量离线包下载与解密工具
│   ├── merge_offline_dbs.py        # 全量离线库合并去重与过滤工具
│   ├── enrich_from_sakura.py       # SakuraPlayer 本地补全工具
│   ├── javdb_magnet_fetcher.py     # JavDB 单番号按需磁力提取器
│   └── javdb_rankings_fetcher.py   # JavDB 榜单定向抓取工具
├── docs/                           # 架构设计规范文档
│   ├── PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md
│   └── MEDIA_LIBRARY_SPEC.md
├── go.mod
├── go.sum
└── README.md
```

---

## 12. 总结与落地实施路径

本架构规范已将用户提出的全部最新要求体系化固化：
1. **彻底清扫历史冗余**：已完全移除原 MediaVault 镜像解包的无用 Linux 根目录、大体积 tar 包及废弃代码，累计释放 1.68 GB 空间；
2. **多源输入去重共存**：30 天增量论坛包、JavDB 榜单补全与在线搜索基于 `offline_magnets.info_hash` 唯一约束天然融合（btih/ed2k/existing 三类统一键，见 §5.4），互不冲突；
3. **JavDB 榜单抓取与自动补全**：支持周榜、月榜、TOP 250 定向收录热门番号并自动优选补充磁力；
4. **刮削结果与失败原因全生命周期审计**：详细记录失败错误码（404、风控、超时等）；
5. **10 天超期智能熔断**：失败资源且发布已超 10 天者，自动标记为放弃刮削，杜绝无效重试；
6. **前端全量刮削与日期范围选择**：管理员可自由指定资源日期范围（如 2025 年至今），一键调度全量刮削；
7. **严格磁力优选准则**：严格践行 `中文字幕 > 破解 > 4K > 有码 > 体积最大 > 唯一磁力` 决策链。

### 12.1 本轮补充闭环（P0/P1/P2）

8. **解密规格修正**：统一为 **AES-256-GCM**（PBKDF2 派生 + nonce/tag 认证），并给出 §5.3 完整步骤；
9. **搜刮接口修正**：JavDB 统一为 `/api/v2/search` + `/magnets` + `/reviews`（含评论区 ed2k）；
10. **刮削状态机闭合**：新增 `is_enriched=3`（部分成功），补全批次筛选 `IN (0,3)`；同时解决 `category`/`publish_date` 在 JavDB 来源的缺失冲突（§7.4）；
11. **播放链路闭环**：新增 §8.0 可播性权威判定、§8.3 Tier1 `existing` 写入契约、§8.4 取链失败/转存中降级（含占位短片与 single-flight）；
12. **运行时表补齐**：新增 §4.3 五张运行表（users/auth_sessions/user_progress/play_sessions/libraries）+ system_settings，以及 §4.4 迁移规范；
13. **用户功能补齐**：新增 §9.1 搜索降级、§9.2 图片代理缓存、§9.3 鉴权与会话、§9.4 进度/收藏/已看；
14. **可运维性**：新增 §10.1 管理端 REST API 清单与 §10.2 日志/健康检查/告警（Webhook 去重与静默）。
