# 115 API 字段参考（Cookie + OpenAPI）

> 本文保存历史实测记录，Cookie、上传、生活事件和签到不是新服务默认范围。新服务的当前能力须按 [外部集成合同](./external_integration_contract.md) 重新验证。包含 client 内部派生字段的样例不是纯原始 HTTP 响应，Go 适配器不得向上游索取包装器才有的字段。

**实测于 2026-05，云下载补测于 2026-08-09**。所有字段名 / 语义来自真实 115 API 响应（用 `config/vault.ini` 的 cookie 和 Redis `mv:auth:<app_id>` 里的 OpenAPI token 跑测试脚本得出）。

写涉及 115 API 字段的代码前**先查此文档不要猜**。新增 / 修改字段引用时实测后更新此文档。

---

# Part A：Cookie 客户端（`app/clients/cookie115/client.py`）

## 1. `list_files(cid, limit, offset, show_dir, ...)` — `GET https://webapi.115.com/files?cid={cid}`

按目录 cid 列出子项；`show_dir=0` 仅文件，`show_dir=1` 包含目录。**cid 必须是目录 id**，传文件 id 时 115 会回退返回根目录（错误数据）。

### 目录项（仅 show_dir=1 时出现）

```json
{
  "cid": "3021356708971035252",   // 目录自身 cid
  "pid": "0",                      // 父目录 cid（根目录为 "0"）
  "aid": "1",
  "n": "media",                    // 目录名
  "fc": 0,                         // 始终为 0 表示目录
  "pc": "fa25el3wbw1b9z4fix",      // 目录的 pickcode
  "t": "1778938176", "te": "...", "tu": "...", "tp": "...", "to": "...",
  "m": 0, "sh": "0", "cc": ""
}
```

**目录判定**：含 `pid` + 无 `fid` → 目录（client.py:1733 `if "pid" not in d` 判定）。

### 文件项（show_dir=0 / 1 都返回）

```json
{
  "fid": "3433704962178298590",    // 文件 id（用 fid 而非 file_id）
  "cid": "3433704962002137818",    // ★ 父目录 cid（不是文件自身！）
  "n": "movie.mkv",
  "s": 31869350649,                // 文件大小（int）
  "pc": "e5fl2duwqwhuxzl3a",
  "fc": 1,                         // 始终为 1 表示文件
  "sha": "C8C5CE68834D...",
  "ico": "mkv",
  "t": "2026-05-21 14:38"
  // 注意：文件项无 pid 字段
}
```

**关键**：文件项 `cid` = **父目录 cid**，不是文件自身 id；自身 id 在 `fid`。

**时间字段（实测）**：文件项同时带 `t`/`te`/`tu`/`tp`。⚠️ `t` 是**格式化日期串**（`"2026-07-02 08:46"`，`int()` 会抛异常），`te`/`tu`/`tp` 才是 **unix 秒字符串**（如 `"1782953197"`）。取时间戳用 `te`（修改时间）等，勿用 `t`。文件项**无** `user_utime`/`upt`（那是 openapi search 接口才有）。

---

## 2. `search_file(search_value, cid, limit, offset)` — `GET /files/search`

按文件名模糊搜索，可选限定 cid 范围。**返回字段已 normalize 含 parent_id**。

```json
[
  {
    "file_id": "3433704962178298590",
    "file_name": "movie.mkv",
    "file_category": "1",                // "1"=文件 / "0"=目录
    "parent_id": "3433704962002137818",  // ★ 真实父目录 cid
    "pick_code": "e5fl2duwqwhuxzl3a",
    "size": "31869350649"
  }
]
```

**Cookie 模式按 file_id 反查 parent 的唯一可靠接口**（见 `get_file_parent_id` 实现）。

---

## 3. `_get_skim_nodes_batch(file_ids)` — `POST /files/file`

批量取文件/目录的简略信息。**支持文件 id 和目录 id**。

```json
[
  {
    "file_id": "3433704962178298590",
    "file_name": "movie.mkv",
    "pick_code": "e5fl2duwqwhuxzl3a",
    "sha1": "C8C5CE68834D...",       // 文件有；目录为空串
    "file_size": "1253954161"        // 文件大小（str）；目录为 "0"
  }
]
```

**重要**：**无 pid / parent_id / cid 字段** — 此接口拿不到父目录信息。要查父用 `search_file` 或调用方维护 pickcode→parent 映射。

---

## 4. `get_file_info(file_id, path)` — `GET /files?cid={file_id}` (内部)

复用 list_files 接口拿单文件/目录的元信息。

### `file_id` 是目录 id ✓

```json
{
  "file_id": "3021356708971035252",
  "file_name": "media",
  "paths": [{"name": "根目录", "cid": "0", "pid": "0", "aid": "1"}, ...],
  "parent_path": "/media/...",
  "parent_id": "0"
}
```

### `file_id` 是文件 id ⚠️ **行为不可靠**

```json
{
  "file_id": "3434765308200629062",
  "file_name": "根目录",                 // ★ 错误数据 —— 实际是根目录的 name
  "paths": [{"name": "根目录", "cid": "0", "pid": "0"}],
  "parent_path": "/",                    // ★ 错的
  "parent_id": "0"                       // ★ 错的，会被误用
}
```

**严禁**对**文件 id** 直接调 `get_file_info`，要拿父目录用 `get_file_parent_id(file_id, file_name)`。

### ⚠️ `get_file_info(path=文件)` 拿不到文件 size（实测）

`get_file_info` 走的是**目录信息接口**（`/open/folder/get_info`，含 `folder_count`/`size_byte` 等聚合字段）。对**文件路径**实测：
- OpenAPI：返回 `size=0`（聚合字段对文件无意义）
- Cookie：返回 `None`

**要拿文件 size/真实信息，必须 `list_files` 父目录、从列表项取**（`file_size`/`fs`/`s`），不能用 `get_file_info`。洗版 `_fetch_file_size` 即按此实现。

---

## 5. `get_file_parent_id(file_id, file_name)`

按 file_id + file_name 拿文件真实父目录 cid。**file_name 必填** — 内部用 search_file 按名查再按 id 匹配过滤。

```python
parent_cid = await client.get_file_parent_id(file_id="3433...", file_name="movie.mkv")
# 返回 "2937813581623081472"（真实父 cid）或 None
```

---

## 6. `get_dir_id(path)` — `GET /files/getid?path=...`

按完整目录路径拿 cid。**path 必须是目录路径**，不支持文件路径。

```json
{"state": true, "id": "3021356708971035252"}    // 成功
{"state": false, ...}                            // 失败 / 不存在 / 不是目录
```

---

## 6.5 `batch_get_pic_urls(sha1_list)` — `POST life.115.com/api/1.0/web/1.0/imgload/get_pic_url`

**按 SHA1 取免鉴权文件直链**，是 nfo / 字幕等非图片文件的下载快车道。
名字叫「图片预览图」。对 nfo / 字幕等非图片文件可按内容 SHA1 取原始字节；图片文件即使
不超过 50MB，也可能返回重编码后的预览图（2026-08-29 生产日志验证），因此图片不走此通道。

**请求**：`data={"rs[]": [SHA1大写, ...]}` + 登录 Cookie（取链需要 Cookie，下载不需要）

**响应**：
```json
{"state": true, "code": 0, "message": "", "data": [
  {"json":  "https://life.115.com/imgload?h=<SHA1>&i=0&t=0&ss=<签名>&tt=<时间戳>",
   "thumb": "https://life.115.com/imgload?h=<SHA1>&i=100&..."}
]}
```

| 实测结论 | 说明 |
|---|---|
| **批量上限 50** | 传 100/200 个只返回前 50 条，**静默截断不报错** —— 必须自行分片 |
| **返回顺序不保证** | 用链接里的 `h=<SHA1>` 反查归属，不能按下标对齐入参 |
| **下载免鉴权** | 不带 Cookie 直接 GET 即可，不占 115 下载配额，可并发（实测 8 并发无限流） |
| **仅限 ≤50MB 的非图片文件** | 超限文件及图片可能返回缩略图/重编码预览图且 SHA1 不符 —— **必须做类型、大小闸门 + SHA1 校验** |
| **`content-type` 恒为 `image/jpeg`** | 谎报类型，不能用它判断内容，只能靠 SHA1 |
| **`i=0` 即原始字节** | `.srt` / `.csf-bk` / `.nfo` 等非图片文件同样逐字节匹配；`i=100` 是缩略图 |
| 未限流 | 无间隔连发 30 次取链 0 失败（247ms/次） |
| 有效期未知 | 链接带 `ss` 签名 + `tt` 时间戳，**不要缓存**，用时现取（取链很便宜） |

**性能**（2026-07-23，40 个真实字幕文件端到端实测）：
免鉴权直链批量取链 + 8 并发 `5.16s` vs 逐个 `ufile/download` + 0.5s 间隔 `43.64s` → **8.5×**。

---

## 7. 其他 Cookie 接口速查

| 接口 | endpoint | 关键返回 |
|---|---|---|
| `delete_files(file_ids)` | `/rb/delete` | `{state: bool}` — **异步操作**，返回 True 不代表立即删除完成 |
| `move_files(file_ids, target_cid)` | `/files/move` | `{state: bool}`（同 pid 移动可能 state=false） |
| `rename_file(file_id, new_name)` | `/files/batch_rename` | `{state: bool, error?: str}` |
| `create_folder(parent_id, name)` | `/files/add` | `{state: bool, cid?: str}`；已存在时 errno=20004 |
| `download_folders(pc)` | `/files/export_dir` | folder items `fid, fn, pid` |
| `download_files_proapi(pc)` | proapi.115.com | file items `pc, pid, fs, fn` |

---

## 8. 上传（uplb 端点，ECDH 加密）

Cookie 模式上传走 uplb，与 OpenAPI 的 `/open/upload/*` 是两套协议。请求体用 ECDH 协商的
AES-128-CBC 加密、响应还套一层 LZ4 block（实现见 `cookie115/ec115.py`）。

| 接口 | endpoint | 关键返回 |
|---|---|---|
| 取上传凭据 | `POST proapi.115.com/app/uploadinfo` | `user_id`, `userkey` —— 签名必需 |
| 初始化上传/秒传 | `POST uplb.115.com/4.0/initupload.php` | `status`, `pickcode`, `bucket`, `object`, `callback{callback,callback_var}`, `sign_key`, `sign_check` |
| OSS STS 凭据 | `GET uplb.115.com/3.0/gettoken.php` | `StatusCode`, `AccessKeyId`, `AccessKeySecret`, `SecurityToken` |
| OSS 接入点 | `GET uplb.115.com/3.0/getuploadinfo.php` | `endpoint`, `gettokenurl` |
| 客户端版本号 | `GET appversion.115.com/1/web/1.0/api/chrome` | `data.win.version_code` |

**`status` 语义**：`1`=需真传（带 bucket/object）、`2`=秒传成功、`7`=需二次认证（带 `sign_key`/`sign_check`）、`4`=错误（看 `statuscode`/`statusmsg`）。

**字段名坑**：uplb 用 `pickcode`/`fileid`，OpenAPI 用 `pick_code`/`file_id`；秒传响应的 `fileid` 常为 0，
需用 `_pc_to_fid(pickcode)` 本地换算。`preid` 不参与请求也不参与签名（仅 OpenAPI 用）。

**⚠️ 表单编码方式本身参与校验（2026-08-28 真实凭据 A/B 实测，四条缺一不可）**：

| # | 规则 | 错法 |
|---|---|---|
| ① | `t` 用**秒**级时间戳，且 `k_ec` 用同一个 t | 毫秒 |
| ② | 表单**按 key 排序**后再 urlencode | 按书写顺序 |
| ③ | **过滤假值**：空串 / 0 的字段不发送（`appid` 恒为 0，故不出现在请求里） | 原样发 `appid=0` |
| ④ | `userkey` 本身要作为**表单字段发送** | 只用它算 `sig`，不发送 |

违反任意一条，115 一律回 `status=4` + `statusmsg="token invalid"`，**而签名算法（盐、公式、
`sig`）完全正确**。这个错误信息完全不指向真正的原因，是本项目排查耗时最长的坑之一。

**⚠️ 版本闸门（实测）**：uplb **同时**校验 UA 里的版本和表单 `appversion`，两者须一致且够新，
否则 `statuscode=403 "请升级到最新版本"`。UA 格式：
`Mozilla/5.0 115disk/{ver} 115Browser/{ver} 115wangpan_android/{ver}`。
版本号**钉死在 `_UPLOAD_APP_VER`**（当前 `36.0.0`），不要改成动态拉取：`app_ver` 参与 token
摘要，而盐是固定值，两者必须同代。`appversion.115.com` 下发的是**桌面客户端安装包版本**
（2026-08-20 起为 `36.0.1`），与上传协议的 app_ver 不是一回事；`proapi.115.com/app/uploadinfo`
响应里的 `app_ver` 才是 115 为上传链路下发的值。

**排查 `token invalid` 的正确顺序**（照错顺序会白费很久）：
1. 先比对**表单编码方式**（上表四条）——最常见，且错误信息毫无提示
2. 再看 `uploadinfo` 是否正常返回 `user_id`/`userkey`、`upload_allowed` 是否为 `true`
   ——能一次排除 cookie 失效、账号限权、限流三种可能
3. 最后才怀疑签名盐 / 版本号

**限流长什么样（2026-07 实测，与上面是两回事）**：短时间内频繁发起上传（尤其 `status=1` 的
真传申请，每发占一个 OSS 预约位）后确实也会回 `token invalid`。区分方法：限流时
`uploadinfo` 的 `upload_allowed` 仍为 `true`，但**冷却后单发能成功**；编码错则**永远失败**，
与时间和用量无关。失败的尝试本身会延长封锁，所以「等一会→试一发→失败」的循环会自己把
封锁续上——排查时每轮只打一发。

---

## 9. 云下载（lixian / clouddownload 端点）

Cookie 云下载与 OpenAPI 是两套协议：添加任务走 RSA 封装的
`https://lixian.115.com/lixianssp/`，其余管理操作走 `clouddownload.115.com/web/`。
`clouddownload.115.com/lixianssp/` 会返回空响应，不能用于添加任务。以下只读能力于
2026-08-09 用真实 Cookie 实测；添加任务端点于 2026-08-10 用真实 ED2K 链接实测。

| 能力 | action | 关键请求 / 返回 |
|---|---|---|
| 详细配额 | `get_quota_package_info` | `count`, `surplus`, `used`, `max_size`, `package`；页面配额必须用此接口，简略接口只有 `quota/total` |
| 任务列表 | `task_lists` | 请求 `page`, `page_size`；返回 `tasks`, `page`, `page_count`, `count` |
| 添加链接 | `add_task_urls` | `lixianssp` RSA 请求；链接字段为 `url[0]`, `url[1]`…，目标目录为 `wp_path_id` |
| 删除任务 | `task_del` | `hash[0]` + `flag`（是否连源文件一起删除） |
| 清空任务 | `task_clear` | `flag`: 0 已完成 / 1 全部 / 2 失败 / 3 进行中 / 4 已完成并删源 / 5 全部并删源 |
| 解析种子 | `torrent` | 请求 `sha1`；文件列表字段为 `torrent_filelist_web`，大小字段为 `torrent_size` |
| 添加 BT | `add_task_bt` | `lixianssp` RSA 请求；`info_hash`, `wanted`, `savepath`, `wp_path_id` |

任务列表字段与前端展示契约一致：`info_hash`, `name`, `size`, `percentDone`, `status`,
`add_time`, `last_update`, `move`, `file_id`, `url`。Cookie 的 `status=4` 表示“搜索资源中”，
客户端归一化为页面的“分配中”状态 `0`；其余状态 `-1/1/2` 分别为失败/下载中/已完成。

BT 解析需在客户端边界归一化：`torrent_size → file_size`、
`torrent_filelist_web → torrent_filelist`。Cookie 接口只需种子 SHA1，现有 OpenAPI 请求中的
`pick_code` 为保持统一方法签名而保留，但 Cookie 请求不发送该字段。

## 10. Cookie 回收站（2026-08-09 实测）

Cookie 回收站使用网页端协议：

| 操作 | endpoint | 请求字段 |
|---|---|---|
| 列表 | `GET https://webapi.115.com/rb` | `aid=7, cid=0, limit, offset, o=dtime, asc=0` |
| 还原 | `POST https://webapi.115.com/rb/revert` | `rid[0]=<回收站条目 id>` |
| 永久删除/清空 | `POST https://webapi.115.com/rb/secret_del` | 单项带 `tid`；清空不带 `tid`；账号启用安全校验时带 `password` |

列表实测顶层为 `{state, data, offset, page_size, count, rb_pass}`，其中 `data` 是条目数组；
条目包含 `id/file_name/file_size/parent_name/cid/dtime/type/ico/status`。客户端必须在边界
归一化成 OpenAPI 回收站的 `data={id: item, offset, limit, count, rb_pass}` 契约，才能复用
现有页面和 Webhook 恢复的分页、同名文件消歧逻辑。

永久删除所需的安全密钥只从运行时环境读取：默认账号用
`MV_115_RECYCLE_SECURITY_KEY`，多实例可用
`MV_115_RECYCLE_SECURITY_KEY_<SLUG>` 覆盖（slug 大写，非字母数字转下划线）。未配置时不发送
`password`，适配已关闭回收站安全校验的账号；服务端要求密钥时必须把失败原样返回，不能
误报清空成功。

## 12. 生活事件（2026-09-02 实测，life_event 插件在用）

`get_life_events()` — `GET https://life.115.com/api/1.0/web/1.0/life/life_list`
（参数 `offset` / `limit` / `tab_type=0` / `start_time` / `end_time`）

返回是**分组**结构，需展开：外层每组一个 `behavior_type` + `update_time`，组内 `items[]`
是该组的具体事件；`account_security` 这类简单事件没有 `items`，事件字段直接挂在组上。

```jsonc
{"state": true, "data": {"count": 24, "list": [
  {"behavior_type": "receive_files", "update_time": 1788317774, "date": "2026-09-02",
   "source": "115生活·网页端", "total": 1,
   "items": [{
     "id": "3508969614023329077",     // 事件 ID（去重游标用，非文件 ID）
     "type": 14,                       // 与 behavior_type 一一对应，见下表
     "file_id": "3508969613578732850",
     "parent_id": "2937813581623081472",
     "file_name": "亲爱的丈夫～完美妻子的谎言～ (2026) {tmdb-323858}",
     "file_category": 0,               // int！0=目录 / 1=文件
     "file_size": 0, "sha1": "",       // 目录恒为 0 / 空
     "pick_code": "fbiaidkrz4syz9ws6k",
     "update_time": 1788317774, "create_time": 1788317774
   }]}
]}}
```

**`behavior_type` ↔ `type` 全表**（与 p115client 的 `BEHAVIOR_NAME_TO_TYPE` 一致）：

| type | behavior_type | 含义 | MV 事件类型 |
|---|---|---|---|
| 1 / 2 | `upload_image_file` / `upload_file` | 上传 | upload |
| 3 / 4 | `star_image` / `star_file` | 标星 | 忽略 |
| 5 / 6 | `move_image_file` / `move_file` | 移动 | move |
| 7-10 | `browse_image` / `browse_video` / `browse_audio` / `browse_document` | 浏览 | 忽略 |
| 14 | `receive_files` | 接收文件（分享转存） | copy |

| 17 | `new_folder` | 新建目录 | mkdir |
| 18 | `copy_folder` | 复制目录 | copy |
| 19 | `folder_label` | 目录打标签 | 忽略 |
| 20 | `folder_rename` | 目录改名 | rename |
| 22 | `delete_file` | 删除 | delete |
| 23 | `copy_file` | 复制文件 | copy |
| 24 | `file_rename` | 文件改名 | rename |

> 注：数字兜底表（`_ITEM_TYPE_MAP`）已覆盖全部变更类事件，所以**名字写错不会立刻出故障**，
> 只会静默走兜底——这正是 `create_folder` / `receive_file` 这类拼错能长期潜伏的原因。
> 排查生活事件问题时不要因为「类型解析看起来正常」就认定映射没问题。

**坑点（写映射前必看）**：

- **115 不按文件/目录分事件名**。目录移动同样是 `move_file`(6)、目录删除同样是
  `delete_file`(22)——`"folder" in behavior_type` 只能作正向信号（`new_folder` /
  `copy_folder` / `folder_rename` 必是目录），**不能**用来判"这不是目录"。
  唯一可靠依据是 `file_category`（int 0=目录）。
- **名字不能凭直觉造**。`receive_files` 是**复数**、新建目录叫 `new_folder` 而不是
  `create_folder`；115 从不下发 `move_folder` / `delete_folder` / `receive_file`。
  写错的名字不会报错，只会静默落到数字兜底或 `unknown`。
- `id` 是**事件 ID**不是文件 ID；游标去重按它，别拿 `file_id` 当唯一键
  （同一文件可以有多条事件）。
- 实测取样：单账号一页 794 条事件，只出现 `upload_file`(2) / `move_file`(6) /
  `receive_files`(14) / `new_folder`(17) / `delete_file`(22) / `copy_file`(23) /
  `file_rename`(24) 七种。

---

# Part B：OpenAPI 客户端（`app/clients/client115/files.py`）

## 1. `get_file_info(file_id, path)` — `POST /open/folder/get_info`

名义是 folder 接口，**实际对文件 id / 文件 path 均兼容**（实测）。

```json
{
  "count": "...", "size": "...", "size_byte": "...", "folder_count": "...",
  "play_long": "...", "show_play_long": "...",
  "ptime": "...", "utime": "...",
  "file_name": "movie.mkv",
  "pick_code": "e5fs6opidsaeezl3a",
  "sha1": "C092FD376B...",
  "file_id": "3433570749013700731",
  "is_mark": "0", "open_time": "0",
  "file_category": "1",              // "1"=文件 / "0"=目录
  "paths": [{                         // ★ 面包屑链（不含 self）
    "file_id": "2937813581623081472",
    "file_name": "最近接收",
    "iss": "0", "pid": "0"
  }],
  "parent_id": "2937813581623081472", // client 内部派生（取 paths[-1].file_id）
  "parent_path": "/最近接收"           // client 内部派生
}
```

**与 Cookie 区别**：OpenAPI 这个接口对**文件 path / 文件 id 均可靠**返回正确 parent_id；cookie 对应接口失效。

---

## 2. `search_file(search_value, cid, limit, offset)` — `GET /open/ufile/search`

```json
[
  {
    "file_id": "3433570749013700731",
    "user_id": "16808628",
    "sha1": "C092FD376B...",
    "file_name": "movie.mkv",
    "file_size": "5622361420",            // 字符串
    "user_ptime": "1779329529",
    "user_utime": "1779532855",
    "pick_code": "e5fs6opidsaeezl3a",
    "parent_id": "2937813581623081472",   // ★ 真实父 cid（与 cookie search 一致）
    "area_id": "1",
    "is_private": 0,
    "file_category": "1",                  // "1"=文件 / "0"=目录
    "ico": "mp4"
  }
]
```

**与 cookie search 对比**：多 `user_id / user_ptime / user_utime / area_id / is_private / ico`；少 `size`（用 `file_size` 代替）。`parent_id / file_id / file_category` 完全对齐。

---

## 3. `list_files(cid, limit, offset, show_dir, order, asc)` — `GET /open/ufile/files`

字段命名**混合**长名（`file_name`/`file_id`/`pick_code`/`file_size`/`file_category`）+ 短名（`fn`/`fid`/`pc`/`fs`/`fc`）。client 内部 `or` 链兜底两种（见 `files.py:305-319`）。

判 dir：`fc == "0"` 或 `file_category == "0"` 或 `is_dir == "1"`（与 cookie 不同—— cookie 用 `'pid' in item`）。

**递归列举（2026-08-19 实测，cid=0 全盘）**：带 `type=N&cur=0` 时接口按**整棵子树**返回该类型的文件，
不再局限于直接子项；不带 `type` 时 `cur=0` 无效（仍只列直接子项）。`limit=1150` 可用（单页 1150 条）。
`o=user_utime&asc=0` 排序生效，返回项按 `upt` 倒序。

| 参数 | 实测结果 |
|---|---|
| `type=4&cur=0` | 全盘 73642 个视频，`count=73642`，一页 1150 条 → 65 页拉完整库；条目含 `fid/fn/pid/pc/fs/sha1/upt/uet/fc="1"`；`.iso` 也算视频（/最近接收 实测） |
| `type=2&cur=0` | 图片（fanart/clearlogo 等）同样递归 |
| `type=1&cur=0` | 「文档」只命中 txt，**nfo / 字幕不算文档**，非媒体伴侣文件不能靠 type 递归拿到 |
| `cur=0`（无 type） | 只返回直接子项，与不带 cur 相同 |
| `show_dir=1&type=…&cur=0` | 递归结果里不含目录项；目录只能逐层列或用 `get_file_info(file_id=pid)` 反查 `paths` |

递归条目**没有 path**，只有 `pid`；要还原完整路径需按 pid 去重后走 `/open/folder/get_info?file_id=`（返回 `paths` 链），
或用逐层列目录时建立的 fid→path 映射。目录项时间字段为 `upt`/`uet`（unix 秒，int），文件项同名。
代码侧：`File115Client.list_files_recursive()` 封装翻页/去重/upt 截断；`services/cloud_dir_cache.CloudDirCache`
维护 fid→路径（持久化 `cloud_dir_nodes` 表）；`services/cloud_media_scan.scan_media_tree()` 组合两者供目录监控用。
实测（/最近接收，53 视频 10 目录）：逐层遍历 11 次请求，递归+热缓存 2 次，递归+冷缓存 11 次。
**upt 语义（实测）**：文件在账号内移动 **不改** upt；复制产生新 fid、新 upt；目录改名会更新目录项自己的 upt。
所以按 upt 截断的增量看不到「移动进来的旧文件」，靠 `count`（子树内该类型总数）对账兜底；`count` 偶有一拍延迟。

---

## 4. `get_file_parent_id(file_id, file_name)`

与 cookie 对称签名。内部走 `search_file` 拿 `parent_id`。**file_name 必填**。

---

## 5. 其他 OpenAPI 接口

| 接口 | endpoint | 返回 |
|---|---|---|
| `delete_files(file_ids)` | POST `/open/ufile/delete` | 历史包装返回 bool；新服务须验证原始业务状态，区分 accepted 与 confirmed_missing |
| `move_files(file_ids, to_cid)` | POST `/open/ufile/move` | `bool` |
| `copy_files(file_ids, pid)` | POST `/open/ufile/copy` | `bool` |
| `rename_file(file_id, new_name)` | POST `/open/ufile/update` | `bool` |
| `create_folder(parent_id, name)` | POST `/open/folder/add` | `dict` 或 None（已存在时 fallback 列父目录查找） |
| `get_dir_id(path)` | 内部调 `get_file_info(path=...)` | `str` cid 或 None |

---

# Part C：字段命名跨接口对照表

| 语义 | webapi /files (cookie) | search_file (cookie) | search_file (openapi) | get_file_info (openapi) | /files/file skim (cookie) |
|---|---|---|---|---|---|
| 文件 id | `fid` | `file_id` | `file_id` | `file_id` | `file_id` |
| 目录 id | `cid` (自身) | `file_id` | `file_id` | `file_id` | `file_id` |
| 文件/目录名 | `n` | `file_name` | `file_name` | `file_name` | `file_name` |
| 父目录 id | 文件 `cid`，目录 `pid` | `parent_id` | `parent_id` | `parent_id` (派生) | ❌ 无 |
| pickcode | `pc` | `pick_code` | `pick_code` | `pick_code` | `pick_code` |
| 文件大小 | `s` (int) | `size` (str) | `file_size` (str) | `size_byte`/`size` | `file_size` (str) |
| sha1 | `sha` | ❌ | `sha1` | `sha1` | `sha1` |
| 类型 | `fc` (0/1 int) | `file_category` ("0"/"1") | `file_category` ("0"/"1") | `file_category` ("0"/"1") | ❌ |

---

# Part C2：回收站 `/open/rb/list`（OpenAPI，2026-07-18 实测）

用 `scripts/probe_115_recycle_bin.py` 实测固化。**响应结构与其它列表端点完全不同，按 list 处理必崩。**

`full_response=True` 时顶层为 `{state, code, message, data}`，**`data` 是 dict，不是 list**：

```jsonc
"data": {
  "3475912707947890552": {          // 键 = 条目 id（即 tid）
    "id": "3475912707947890552",    // revert/del 用的 tid
    "file_name": "xxx.txt",
    "file_size": "34",              // str
    "sha1": "ECF6ED1C...",
    "pick_code": "e54kobbvsua58zl3a",
    "cid": 0,                       // 删除前的父目录 id（int，0=根目录）
    "parent_name": "根目录",         // 删除前的父目录名 ← 同名条目消歧就靠它
    "dtime": "1784377084",          // 删除时间戳（str）← 同名多条取最新
    "type": "1", "ico": "txt", "status": "0",
    "d_img": "", "thumb_url": "", "isv": 0, "muc": ""
  },
  "offset": 0, "limit": 5,          // ← 元数据键与条目键混在同一层
  "count": "1171",                  // 总条目数（str）
  "rb_pass": 1
}
```

**必须遵守**：
- 取条目：`[v for v in data.values() if isinstance(v, dict)]` —— 直接 `for k in data` 拿到的是键（含 `offset`/`count` 等元数据），`items.extend(data)` 得到的是字符串列表，后续 `item.get(...)` 会 `AttributeError`
- 翻页终点看 `data["count"]`（str，需 int 转换），不要靠"返回数 < limit"——单页里混了 4 个元数据键，条目数永远比 `len(data)` 少
- **按文件名找条目必须消歧**：回收站里同名条目很常见（不同剧的 `S01E01.nfo`）。用 `parent_name` 校验原目录，多条命中再按 `dtime` 取最新，否则会 revert 错文件

## 11. 签到与积分商城（2026-08-11 实测，checkin_115 插件在用）

请求头同普通 web API（Cookie + `Mozilla/5.0 115Browser/23.9.3.2`），无需 appversion 闸门。

**签到** `proapi.115.com/android/2.0/user/points_sign`：
- `GET` 查状态 → `{"state": true, "data": {"is_sign_today": 0/1, "continuous_day": n, "sign_rules": {...}}}`；未登录时 `{"state": false, "error": "请重新登录", "errno": 99}`
- `POST` 提交签到，表单 `token` + `token_time`（秒级时间戳），`token = sha1("{UID}-Points_Sign@#115-{token_time}")`，UID 取 Cookie 里 `UID=` 的数字前缀

**积分商城** base `points.115.com/api/1.0/web/1.0`（成功 `state=1`，失败 `state=0` + `code` + `message`）：
- `GET /user/balance` → `data.balance`（枫叶余额）、`data.redeem_count`（当月已兑次数）
- `GET /goods/get_goods_list?page&limit&category`、`GET /goods/get_category_list`（cid=71 是「115服务」分类，含空间/配额类商品；商品列表**无需登录**即可访问）
- `GET /goods/get_goods_detail?goods_id` → `price_points`（普通价）/`price_member_points`（会员价）/`user_limit_count`/`exchange_desc`；**20GB 空间 = goods_id 524**，普通价 2750 / 会员价 550，官方限每月 2 次
- `POST /order/buy` 表单 `goods_id` 一步下单；余额不足 → `code=43301004, message="枫叶不足"`；`/order/pre_buy` 为预下单（POST，GET 会 405）
- 坑：商品的 `can_redeem` **不等于**「余额够不够」（余额 0 时多数商品仍 true），判可兑以 buy 响应为准

# Part D：字段加固原则（写代码时遵守）

1. **判文件/目录**：
   - cookie list_files → `'pid' in item and 'fid' not in item`（目录）
   - cookie/openapi search → `file_category == '0'`（目录）
   - openapi list_files → 同时检查 `fc == '0' or file_category == '0' or is_dir == '1'`

2. **拿文件父 id**（区分原始接口与旧包装器）：
   - 两端 client 都实现了 `get_file_parent_id(file_id, file_name)`，内部走 search_file
   - Cookie 不可直接对文件 id 调 `get_file_info`（可能返回根目录假数据）；OpenAPI 按 Part B 的已验证 paths 推导父 ID，不能将 Cookie 限制扩展到所有接口
   - **不要**用 `_get_skim_nodes_batch`（cookie 该接口无 parent 字段）
   - 用 `_resolve_parent_cid(fc, file_id, file_name)` 自动分发

3. **拿文件自身 id**：
   - cookie list_files → `fid`；search / skim / openapi → `file_id`
   - 跨接口 fallback：`item.get('file_id') or item.get('fid') or item.get('id')`

4. **115 异步删除**：旧包装器 True 仅能作为已接受线索。新服务保存资产及任务身份，间隔至少 1～2s 对账，只有目标确认不存在才完成；确认超时转 reconcile，不能清空 ID 或扩大删除范围。详见主设计 §8.2。

5. **改字段或新增方法时**：先实测（用 `config/vault.ini` 真实 cookie + Redis `mv:auth:<app_id>` 真实 OpenAPI token 跑测试脚本），把发现的字段补充进本文档。


## Cookie 登录状态复核（2026-09-06 只读实测）

- `GET https://my.115.com/?ct=guide&ac=status`：3 个已配置有效 Cookie 均返回 HTTP 200、`{"state": true}`。匿名与伪造无效 Cookie 返回 HTTP 302，跳转至 www.115.com 登录入口；**302 自身不作为失效依据，不跟随 Location**。
- 固定只读备用端点 `GET https://my.115.com/?ct=ajax&ac=nav`：上述有效 Cookie 均返回 HTTP 200、`state: true` 与用户数据；匿名及伪造 Cookie 返回 `{"state": false, "data": [], "error": "请先登录,后操作！"}`。
- 后台复核两轮均使用原请求 Cookie，只有明确未登录两次才通知。HTTP 异常、风控、解析失败、字段不符合契约为 unknown。没有实际使用户 Cookie 失效，未覆盖全部客户端类型。
- 不使用 `check/sso`：p115client 的 `login_check_sso` 源码注明可能使同设备其他 Cookie 失效。来源：https://github.com/ChenyangGao/p115client/blob/main/p115client/client.py 。


## `share/snap` 失败码（2026-09-10 只读实测）

`GET https://webapi.115.com/share/snap`（浏览分享内容）失败时同时返回 `errno` 与中文
`error`，两者含义差别很大，处置也完全不同 —— **不要只把 errno 记进日志**：

| errno | `error` | 触发条件 | 处置 |
|---|---|---|---|
| `990002` | 参数错误。 | `share_code` **格式非法**（不是 11 位 `sw…` 分享码，例如把别家网盘的分享 id 当成 115 分享码发过来） | 链接解析有问题，重试无用 |
| `4100012` | 请输入访问码 | 分享设了访问码但 `receive_code` 为空 | 补上访问码即可，**不是**死分享 |
| `4100010` | 分享已取消 | 发布者取消了分享 | 死分享，不再重试 |
| `4100026` | 该文件分享链接不存在或已被删除 | 分享码格式合法但分享不存在 | 死分享，不再重试 |

对照实测：同一条有效分享带访问码 → `state=true`；去掉访问码 → `4100012`（而不是
990002）。因此 **`990002` 只指向分享码本身不合法，与访问码缺失无关**。

`_safe_remote_failure`（`app/services/subscription_search.py`）据此同时保留 `error` 与
`errno`：`_attempt_is_permanent_failure` 靠 `error` 里的「已取消 / 删除 / 不存在」等
特征词判定死分享，只留 errno 会让死链接每轮重试、烧光订阅的 `transfer_max_per_run` 额度。
