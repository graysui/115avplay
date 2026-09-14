# Emby 客户端协议兼容接口规范（虚拟服务端）

本文档基于 `MediaVault` 的 `media_library_emby` 插件及 Emby 官方客户端（如 Infuse / VidHub / Emby Web / Apple TV 等）实测交互提取。

---

## 1. 认证与握手接口 (Authentication & Handshake)

### 1.1 系统公开信息
- **请求**: `GET /emby/system/info/public`
- **响应**:
```json
{
  "ServerName": "GoMediaVault",
  "Version": "4.8.0.0",
  "Id": "d3b07384d113edec49eaa6238ad5ff00",
  "OperatingSystem": "Linux",
  "StartupWizardCompleted": true
}
```

### 1.2 系统完整信息
- **请求**: `GET /emby/system/info` (带 `X-Emby-Token` 或 `api_key`)
- **响应**: 包含 `SystemInfo`、`ServerName`、`WebSocketPortNumber` 等。

### 1.3 用户认证 (登录)
- **请求**: `POST /emby/users/authenticatebyname`
- **请求头**: 
  - `X-Emby-Authorization`: `MediaBrowser Client="Infuse", Device="Apple TV", DeviceId="...", Version="7.7"`
- **请求体**: `{"Username": "admin", "Pw": "..."}`
- **响应**:
```json
{
  "User": {
    "Name": "admin",
    "Id": "00000000000000000000000000000001",
    "HasPassword": true,
    "Configuration": {
      "PlayDefaultAudioTrack": true
    },
    "Policy": {
      "IsAdministrator": true,
      "EnableContentDownloading": true
    }
  },
  "AccessToken": "sec_token_xxxxxxxxxxxx",
  "ServerId": "d3b07384d113edec49eaa6238ad5ff00",
  "SessionInfo": {
    "Id": "session_xxxxxx"
  }
}
```

### 1.4 当前用户信息
- **请求**: `GET /emby/users/me` 或 `GET /emby/users/{UserId}`
- **响应**: 返回 User 对象。

---

## 2. 媒体库目录与条目 (Libraries & Items)

### 2.1 根媒体文件夹
- **请求**: `GET /emby/library/mediafolders`
- **响应**:
```json
{
  "Items": [
    {
      "Name": "电影",
      "ServerId": "d3b07384d113edec49eaa6238ad5ff00",
      "Id": "lib_movies_1",
      "Type": "CollectionFolder",
      "CollectionType": "movies",
      "IsFolder": true
    }
  ],
  "TotalRecordCount": 1
}
```

### 2.2 条目计数
- **请求**: `GET /emby/items/counts`
- **响应**: `{"MovieCount": 1280, "SeriesCount": 0, "EpisodeCount": 0}`

### 2.3 媒体条目查询 (列表与检索)
- **请求**: `GET /emby/users/{UserId}/items`
- **常见 Query 参数**:
  - `ParentId`: 父目录 ID (不传或为根目录时查所有根条目)
  - `IncludeItemTypes`: 过滤类型（如 `Movie`）
  - `Recursive`: 是否递归子项 (`true`)
  - `SortBy`: 排序字段 (`DateCreated`, `SortName`, `PremiereDate`)
  - `SortOrder`: `Ascending` / `Descending`
  - `StartIndex`: 分页起始索引（从 0 开始）
  - `Limit`: 单页数量
  - `SearchTerm`: 关键词搜索
- **响应**:
```json
{
  "Items": [
    {
      "Name": "IPX-123 美丽的妻子",
      "OriginalTitle": "IPX-123",
      "ServerId": "d3b07384d113edec49eaa6238ad5ff00",
      "Id": "mov_1001",
      "Type": "Movie",
      "PremiereDate": "2024-05-01T00:00:00.0000000Z",
      "ProductionYear": 2024,
      "Overview": "番号介绍剧情...",
      "CommunityRating": 8.5,
      "RunTimeTicks": 72000000000,
      "IsFolder": false,
      "ImageTags": {
        "Primary": "img_mov_1001_p",
        "Backdrop": "img_mov_1001_b"
      },
      "UserData": {
        "PlaybackPositionTicks": 0,
        "PlayCount": 0,
        "IsFavorite": false,
        "Played": false
      },
      "MediaSources": [
        {
          "Id": "mag_uuid_01",
          "Name": "[中文字幕] 4K 压制中字 (8.0 GB)",
          "Container": "mp4",
          "Size": 8589934592,
          "SupportsDirectPlay": true,
          "SupportsDirectStream": true,
          "SupportsTranscoding": false
        },
        {
          "Id": "mag_uuid_02",
          "Name": "[中文字幕] 1080P 精翻中字 (4.2 GB)",
          "Container": "mp4",
          "Size": 4509715660,
          "SupportsDirectPlay": true,
          "SupportsDirectStream": true,
          "SupportsTranscoding": false
        },
        {
          "Id": "mag_uuid_03",
          "Name": "[亚洲有码] 4K 原盘重制 (12.4 GB)",
          "Container": "mkv",
          "Size": 13314398617,
          "SupportsDirectPlay": true,
          "SupportsDirectStream": true,
          "SupportsTranscoding": false
        }
      ]
    }
  ],
  "TotalRecordCount": 1
}
```

### 2.4 最近添加与继续观看
- **最新添加**: `GET /emby/users/{UserId}/items/latest?Limit=16&IncludeItemTypes=Movie`
- **继续观看**: `GET /emby/users/{UserId}/items/resume?Limit=12`
- **下一集 (电视剧用)**: `GET /emby/shows/nextup`

---

## 3. 图片与海报 (Images)

- **主海报**: `GET /emby/items/{ItemId}/images/Primary`
- **背景图**: `GET /emby/items/{ItemId}/images/Backdrop`
- **响应**: 直接输出图片二进制（JPEG/WebP/PNG）或 HTTP 302 重定向至图床/本地静态缓存。

---

## 4. 核心播放流与 302 重定向 (Streaming & Playback)

### 4.1 视频播放流
- **请求**: `GET /emby/videos/{ItemId}/stream.{Container}?MediaSourceId={MediaSourceId}`
- **请求**: `GET /emby/videos/{ItemId}/stream?static=true&MediaSourceId={MediaSourceId}`
- **参数说明**:
  - `MediaSourceId`: 客户端选中的特定版本源 ID (如 `mag_uuid_03`)；若不传则由服务端按照媒体库上下文或全局优先级挑选默认最优源。
- **逻辑**:
  1. 根据 `ItemId` 与 `MediaSourceId` 查找到对应的磁力/115 资源记录；
  2. 若该资源已在 115 落地且有效（有 `pick_code` 且 `is_available = 1`），调用 115 OpenAPI `/open/ufile/downurl` 获取高速直链；
  3. 若尚未转存或临时文件已过期清理，触发后台异步 115 离线秒传与最大视频探测；
  4. 返回 **`HTTP 302 Found`**，`Location: <115直链URL>`。

### 4.2 客户端进度上报
- **开始播放**: `POST /emby/sessions/playing`
- **播放中进度**: `POST /emby/sessions/playing/progress`
- **停止播放**: `POST /emby/sessions/playing/stopped`
- **请求体**:
```json
{
  "ItemId": "mov_1001",
  "PositionTicks": 1500000000,
  "PlayMethod": "DirectPlay",
  "IsPaused": false
}
```
- **服务端处理**: 将 `PositionTicks`（1秒 = 10,000,000 Ticks）更新到数据库 `user_playback_progress` 表，并记录播放日志。
