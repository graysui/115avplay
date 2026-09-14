# 115 官方 OpenAPI 目录树获取、番号识别与自动清理联动规范
## (115 OpenAPI Tree Scan, Code Extraction & Sync Janitor Specification)

本文档补充并细化 115 官方 OpenAPI 在**已有影视目录树定时扫描**、**文件夹名称识别番号**、**最大视频锁定**、以及**临时离线文件定期自动清理与状态联动**的完整技术契约。

---

## 1. 115 目录树获取与扫描机制 (Tree Fetching API)

在 115 官方 OpenAPI 下，获取目录及其子项主要有两种策略：

### 策略 A：整树递归列举视频文件 (适合快速对账全量视频)
- **请求方法**: `GET https://proapi.115.com/open/ufile/files`
- **请求头**: `Authorization: Bearer <access_token>`
- **Query 参数**:
  - `cid`: 目标影视根目录的 CID (如 `3021356708971035252`)
  - `type=4`: **仅列出视频文件** (排除字幕、图片、txt 等)
  - `cur=0`: **开启整棵子树全量递归** (不仅直接子项，递归所有层级)
  - `limit=1150`: 单页最大拉取量 (115 官方最高支持 1150)
  - `offset=0`: 翻页游标
  - `o=user_utime&asc=0`: 按更新时间倒序
- **响应结构**:
```json
{
  "state": true,
  "count": 1420,
  "data": [
    {
      "fid": "3433570749013700731",
      "fn": "IPX-123.mp4",
      "fs": 5622361420,
      "pc": "e5fs6opidsaeezl3a",
      "pid": "2937813581623081472",
      "sha1": "C092FD376B...",
      "fc": "1",
      "upt": 1779532855
    }
  ]
}
```
- **关键字段与坑点**:
  - `cur=0` 返回的视频项**不直接包含完整 path**，但包含直接父目录 `pid`。
  - 要知道该视频属于哪个文件夹（以便通过文件夹名提取番号），需要获取其父目录 `pid` 的名称。

---

### 策略 B：层级逐层列举文件夹 (最适合文件夹命名番号的结构)
通常用户的 115 影视目录结构为：
```
115 影视根目录 (CID: 1000)
├── [IPX-123] 绝美妻子相泽南 (目录 CID: 2001)
│   ├── IPX-123.mp4 (最大视频，5.2 GB, pick_code: pc_123)
│   ├── IPX-123.nfo
│   └── cover.jpg
└── [SSNI-999] 三上悠亚 (目录 CID: 2002)
    └── SSNI-999.mkv (最大视频，6.1 GB, pick_code: pc_999)
```

#### 步骤 1：列出根目录下的所有子文件夹
- **请求**: `GET https://proapi.115.com/open/ufile/files?cid={root_cid}&show_dir=1&limit=1150`
- **过滤目录项**:
  - 判定条件：`fc == "0"` 或 `file_category == "0"` 或 `is_dir == "1"`
  - 提取子目录属性：
    - `file_id` (或 `cid` / `fid`): 子文件夹自身 CID（如 `2001`）
    - `file_name` (或 `fn` / `n`): 文件夹名称（如 `"[IPX-123] 绝美妻子相泽南"`）

#### 步骤 2：对每个子文件夹列举内部视频
- **请求**: `GET https://proapi.115.com/open/ufile/files?cid={sub_cid}&show_dir=0&type=4`
- **提取属性**:
  - 遍历该子目录下所有视频文件，剔除预告片、Sample、短片；
  - 找到 `file_size` (或 `fs`) 最大的主片，提取其 `pick_code`, `file_name`, `file_size`, `file_id`。

---

## 2. 文件夹名称识别番号算法 (AV Code Regex Extraction)

根据业界常见命名习惯，文件夹名通常包含番号以及中文说明、演员名、日期或分辨率标签。例如：
- `[IPX-123] 美丽妻子`
- `SSNI-999_CH_HD`
- `FC2-PPV-1234567 偷拍流出`
- `MIDV-045 [中文字幕]`
- `carib-010124-001 顶级步兵`
- `259LUXU-1234 素人`

### 正则表达式规则库 (Go 语言实现)

```go
package scanner

import (
    "regexp"
    "strings"
)

var (
    // 常见标准番号: ABC-123, ABCD-123, ABC-0123, 259LUXU-1234, 111122-333
    reStandardCode = regexp.MustCompile(`(?i)\b([a-z0-9]{2,10})-?([0-9]{2,8})\b`)
    // FC2 系列: FC2-PPV-1234567, FC2PPV-1234567
    reFC2Code = regexp.MustCompile(`(?i)fc2(?:-?ppv)?-?([0-9]{5,8})`)
    // 无码/加勒比系列: 123456-789, carib-123456-789
    reUncensored = regexp.MustCompile(`(?i)\b([0-9]{6})[-_]([0-9]{3})\b`)
)

// ExtractCodeFromFolderName 从文件夹名提取规范化番号
func ExtractCodeFromFolderName(name string) string {
    name = strings.TrimSpace(name)
    
    // 1. 优先匹配 FC2
    if m := reFC2Code.FindStringSubmatch(name); len(m) > 1 {
        return "FC2-PPV-" + m[1]
    }
    
    // 2. 匹配无码日期型 (如 010124-001)
    if m := reUncensored.FindStringSubmatch(name); len(m) > 2 {
        return m[1] + "-" + m[2]
    }
    
    // 3. 匹配标准字母-数字番号 (如 IPX-123, SSNI-999, 259LUXU-1234)
    if m := reStandardCode.FindStringSubmatch(name); len(m) > 2 {
        prefix := strings.ToUpper(m[1])
        number := m[2]
        // 排除常见分辨率干扰词 (如 1080-720, H264-1080)
        if prefix != "1080" && prefix != "720" && prefix != "H264" && prefix != "H265" {
            return prefix + "-" + number
        }
    }
    
    return ""
}
```

---

## 3. 115 OpenAPI 物理删除接口与规范 (Deletion API)

用于**临时离线转存文件过期后的自动物理清理**。

- **Endpoint**: `POST https://proapi.115.com/open/ufile/delete`
- **Headers**:
  - `Authorization: Bearer <access_token>`
  - `Content-Type: application/x-www-form-urlencoded`
- **Request Body**:
  - `file_ids`: 支持逗号分隔的多个 `file_id` 或文件夹 `cid`。例如 `file_ids=3433570749013700731`
- **Response**:
```json
{
  "state": true,
  "code": 0,
  "message": "",
  "data": {
    "count": 1
  }
}
```
- **核心注意事项**:
  1. **异步性**：115 接口返回 `state: true` 代表任务已投递到 115 后台垃圾回收队列，并非毫秒级物理抹除。连续操作同路径目录时应保留 1~2 秒冷却。
  2. **安全白名单拦截**：清理逻辑**严禁**对已有永久影视目录执行删除！程序内部必须设置安全闸门：**仅允许删除位于 `system_settings` 中配置的 `temp_transfer_cid`（临时转存目录）之下的子项**。

---

## 4. 数据库联动与清理时钟状态机 (Lifecycle State Machine)

```
                       [扫描到 115 已存在目录]
                                │
                                ▼
                   ┌─────────────────────────┐
                   │ source_type: 'existing' │
                   │ storage_cid: <已有目录CID>│
                   │ target_pick_code: 有效  │
                   │ temp_expire_at: NULL    │
                   │ is_available: 1         │
                   └────────────┬────────────┘
                                │
         ┌──────────────────────┴──────────────────────┐
         │ 定时目录树同步                              │ 发现 115 中文件已被删除
         ▼                                             ▼
   [更新文件信息/保持]                           ┌─────────────────────────┐
                                                 │ target_pick_code: NULL  │
                                                 │ is_available: 0         │
                                                 │ (防止 302 播放死链)     │
                                                 └─────────────────────────┘

----------------------------------------------------------------------------------

                       [磁力临时离线转存]
                                │
                                ▼
                   ┌─────────────────────────┐
                   │ source_type: 'temporary'│
                   │ storage_cid: <临时转存CID>│
                   │ temp_expire_at: NOW+TTL │
                   │ transfer_status: 2 (就绪)│
                   │ is_available: 1         │
                   └────────────┬────────────┘
                                │
                                │ 定时 Janitor 检查: NOW >= temp_expire_at
                                ▼
                   ┌─────────────────────────┐
                   │ 1. 调 OpenAPI 物理删除   │
                   │    POST /open/ufile/del │
                   │ 2. 重置数据库记录:      │
                   │    target_pick_code=NULL│
                   │    transfer_status=0    │
                   │    is_available=0       │
                   └─────────────────────────┘
                                │
                                │ 下次用户又想看这部电影时
                                ▼
                   ┌─────────────────────────┐
                   │ 重新触发 115 磁力离线   │
                   │ 重新生成临时文件与直链   │
                   └─────────────────────────┘
```

> **与主规范的对应关系**：上述 `temp_expire_at` 的 TTL **并非硬编码 7 天**，而是由主规范 `system_settings.cleanup_ttl_days` 配置（默认 7 天）；`is_available` 为**派生字段**，其权威判定规则见 `PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md` **§8.0 可播性权威判定**，任何路径不得直接单独改写该字段，须经 `recomputeAvailability()` 重算。

---

## 5. 对账与自愈原则 (Self-Healing & Reconcile)

1. **防死链保护**：客户端点击播放取直链时，若 115 OpenAPI 报错 `file not found` 或 `errno != 0`，服务端立即捕获并将该记录的 `is_available` 设为 `0`、`target_pick_code` 设为 `NULL`，同时自动降级 fallback 到重新转存磁力，不给客户端返回破碎的 302。（`is_available` 的改写须经主规范 §8.0 的 `recomputeAvailability()` 统一重算）
2. **零垃圾残留**：每次执行定时清理前，从 DB 读取所有已到期的 `target_file_id` 和转存任务生成的目录 ID，批量发送给 115 删除，彻底释放 115 空间。
