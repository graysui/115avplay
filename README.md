# 115AVPlay / MediaVault

面向 115 网盘的高性能 Go 虚拟媒体服务器。通过纯 DirectPlay（无转码、无重封装、短时 302 302 重定向 CDN 流）为 Infuse、VidHub 及 Emby 兼容客户端提供高速媒体浏览、多版本智能优选与无缝跨端续播。

内置 Vue 3 管理后台（SPA 纯静态无外部运行时依赖，嵌入单二进制程序），支持 115 OAuth 授权绑定、AVDB 全量/增量元数据同步、JavDB 智能抓取与防封控、冷热资源多级就绪度调度、双连接池 SQLite WAL 数据库架构与自动化容灾维护。

---

## 核心特性

- **纯 DirectPlay 架构**：全链路零转码、零服务端 CPU 编解码开销；客户端直连 115 CDN 高速回源播放（支持 MP4 / MKV 容器及内嵌字幕）。
- **三级就绪度资源调度 (Tier 1/2/3)**：
  - **Tier 1 (永久就绪)**：已存在于永久库目录的资产，1秒内返回 302 直链。
  - **Tier 2 (临时复用就绪)**：已转存并处于有效 TTL 内的资产，立即秒开播放。
  - **Tier 3 (冷资源转存)**：未入库但有可用磁链，自动提交 115 离线下载；8 秒内未就绪时返回标准 Emby 提示，支持管理端一键预热与批量准备。
- **Emby 协议全兼容**：
  - 完美适配 Apple TV / iOS / macOS / Android 上的 Infuse、VidHub 及 Emby 客户端。
  - 虚拟媒体库分类（中文字幕、高清4K、破解、步兵无码、FC2等）。
  - 用户独立播放进度、收藏夹、已播标记与 90% 完播判定。
- **高并发与防抖合并 (Coalescing & Deduplication)**：
  - 同一影片或版本的并发请求自动合并为单一离线任务，杜绝重复转存。
  - JavDB 全局单一令牌桶限流与熔断保护（严格遵守抓取延迟与反爬规则）。
- **双连接池 SQLite (modernc.org/sqlite, CGO_ENABLED=0)**：
  - 单写连接互斥事务 + 多只读连接池，WAL 模式并发无锁读。
  - 独立 AES-256-GCM 主密钥加密存储网盘令牌与敏感配置。
- **全生命周期安全清理 (Janitor & Retention)**：
  - 租约保护：播放中的活动会话阻止 TTL 资源被 Janitor 清理。
  - 严格根目录校验与删除双向确认 (`ConfirmMissing`)，坚决不误删用户个人文件。
  - 磁盘水位监控与图片 LRU 缓存自动修剪。

---

## 快速上手与运行

### 1. 编译构建

项目支持 Windows、Linux (amd64 / arm64) 原生无 CGO 依赖构建：

```bash
# 本地编译单二进制文件
go build -tags "netgo,osusergo" -ldflags="-s -w" -o bin/mediavault ./cmd/server

# 交叉编译 Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags "netgo,osusergo" -ldflags="-s -w" -o bin/mediavault-linux-amd64 ./cmd/server

# 交叉编译 Linux arm64 (适合 NAS / 树莓派)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags "netgo,osusergo" -ldflags="-s -w" -o bin/mediavault-linux-arm64 ./cmd/server
```

### 2. Docker 镜像构建与启动

项目提供生产级多架构极简 Dockerfile（基于 Alpine Linux，内嵌 CA 根证书与时区数据）：

```bash
# 构建镜像
docker build -t mediavault:latest .

# 启动容器
docker run -d \
  --name mediavault \
  -p 8096:8096 \
  -v /opt/mediavault/data:/app/data \
  -e MV_ADMIN_PASSWORD=your_secure_password \
  mediavault:latest
```

---

## 配置说明

服务支持通过命令行参数、环境变量或管理后台进行配置（环境变量优先级高于数据库设置）。

### 常用环境变量

| 变量名 | 默认值 | 描述 |
|---|---|---|
| `MV_DATA_DIR` | `./data` | 数据存放目录（包含 SQLite 数据库、图片缓存、日志、主密钥） |
| `MV_LISTEN` | `0.0.0.0:8096` | 监听地址及端口 |
| `MV_MASTER_KEY_FILE` | `./data/master.key` | AES-256-GCM 主密钥文件路径（首次运行自动生成） |
| `MV_ADMIN_PASSWORD` | *(自动生成)* | 首次启动初始管理员 `admin` 的密码；若未配置，将在控制台日志打印一次性密码 |
| `MV_PUBLIC_URL` | *(空)* | 反向代理时的公网基准 URL（如 `https://media.example.com`） |
| `MV_LOG_LEVEL` | `INFO` | 日志级别：`DEBUG`, `INFO`, `WARN`, `ERROR` |

---

## 初始化与 115 授权流程

1. **启动服务**：
   运行 `./mediavault`。初次启动时会自动创建数据库 schema 并初始化系统管理员用户。
2. **访问管理后台**：
   在浏览器中打开 `http://localhost:8096/web/`，使用用户名 `admin` 及初始密码登录。
3. **绑定 115 账号**：
   - 进入 **设置** -> **115 授权** 页面。
   - 系统提供 **OAuth 设备码流程**（展示二维码与配对码）或 **Cookie / 凭据输入**。
   - 完成授权后，系统将自动安全持久化加密后的 Access Token 与 Refresh Token，并在后台自动刷新。
4. **配置临时转存根目录**：
   - 在 115 网盘中新建一个用于临时缓存的空目录（例如 `MediaVault_Temp`）。
   - 将该目录的 CID 填入管理控制台的 `temp_transfer_cid` 设置中（注意：临时目录严禁设置为网盘根目录 0 或与永久库重叠）。
5. **连接客户端**：
   - 打开 **Infuse** 或 **VidHub**。
   - 新增服务器选择 **Emby**。
   - 服务器地址：`http://<您的IP>:8096`。
   - 用户名与密码：在管理后台创建的普通用户凭据。

---

## 运维与健康检查

- **存活探测**：`GET /healthz`（返回 200 及服务基本运行状态）。
- **就绪探测**：`GET /readyz`（返回 200 表示数据库与核心组件已就绪，未授权 115 不会阻塞此接口）。
- **详细运行指标**：`GET /api/v1/status`（需管理员权限，返回 115 绑定状态、JavDB 熔断器状态、磁盘余量、队列长度与系统内存）。

---

## 运行约束与边界 (Constraints)

1. **纯 DirectPlay**：
   不支持任何形式的 CPU/GPU 实时重编码。客户端需具备本地解封装与解码能力（Infuse、VidHub 原生完美支持绝大部分封装格式与音轨）。
2. **不支持非线性媒体格式**：
   不支持原盘 ISO、VOB、BDMV 文件夹及 STRM 分段流。此类资源会在扫描阶段自动被过滤，避免客户端解析崩溃。
3. **冷资源缓冲机制**：
   未入库冷资源的离线下载依赖 115 云端任务调度。首次点播若 8 秒内未完成转存，客户端将提示准备状态；资源转存就绪后用户可重新点击播放。推荐使用管理后台的“资源预热”功能进行批量提前准备。
