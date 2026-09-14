# 首版 Emby 适配协议

这是新服务的目标契约，首版仅验证指定版本Infuse/VidHub。旧提取文本不是完整协议实现，G0/EC-1记录实际交互后冻结必要别名和字段。业务状态见[主设计](../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md)，新增客户端必须另测。

## 1. 路由与鉴权

支持/emby前缀和无前缀别名；静态段、查询参数名ASCII不区分大小写，值及ID保持原样。使用显式别名/规范化路由表，不能把整个路径连同ID小写。

公开：GET /emby/system/info/public、POST /emby/users/authenticatebyname。其他端点需有效audience=emby票据和enabled用户。凭据支持X-Emby-Token、api_key、X-Emby-Authorization/Authorization的MediaBrowser Token参数；同时出现且不一致则拒绝。任何UserId必须为本人，MediaSourceId必须属于ItemId。

ServerId由schema_meta稳定UUID提供，Version固定兼容声明4.8.0.0不代表完整实现。ItemId=mov_+base64url(code UTF-8)，MediaSourceId=src_+base64url(resource_key UTF-8)，无padding；decode后严格校验。库ID取libraries，升级/重启不随意重建。

公共SystemInfo返回ServerName/Version/Id/StartupWizardCompleted；登录接受{Username,Pw}，返回User、AccessToken、ServerId、SessionInfo。AccessToken明文只在登录响应发一次，DB存SHA256；User.Policy.IsAdministrator照实际用户，不默认true。

## 2. 必需端点

| 方法 | 路径（均可带/emby） | 内容 |
|---|---|---|
| GET | /system/info/public、/system/info、/system/endpoint | 公共/私有握手、服务地址 |
| POST | /users/authenticatebyname | 登录；X-Emby-Authorization携带客户端/设备版本 |
| GET | /users/me、/users/{uid} | 本人User DTO与Policy |
| GET | /users/{uid}/views、/library/mediafolders | 六个可重叠CollectionFolder |
| GET | /users/{uid}/items、/items | Items/TotalRecordCount/StartIndex；/items的UserId只能为本人 |
| GET | /users/{uid}/items/{id}、/items/{id} | Movie详情、版本、图片及当前用户UserData |
| GET/POST | /items/{id}/playbackinfo | 协商MediaSourceId/DeviceProfile/StartTimeTicks，生成PlaySessionId |
| GET/HEAD | /videos/{id}/stream、/videos/{id}/stream.{container} | 指定版本Resolver、短时302；HEAD无新任务副作用 |
| GET | /items/{id}/images/Primary、/items/{id}/images/Backdrop | 鉴权图片、索引/ETag/占位图 |
| POST | /sessions/capabilities、/sessions/capabilities/full | 记录客户端能力；不因此宣称服务器支持转码 |
| POST | /sessions/playing、/sessions/playing/progress、/sessions/playing/stopped | 播放会话/进度上报 |
| POST | /sessions/logout | 撤销当前票据 |
| POST/DELETE | /users/{uid}/favoriteitems/{id}、/users/{uid}/playeditems/{id} | 收藏/手动已看 |
| GET | /users/{uid}/items/resume、/users/{uid}/items/latest | 继续观看、最新 |
| GET | /items/resume、/items/latest | 带本人UserId的兼容别名 |
| GET | /items/counts、/shows/nextup | 去重MovieCount；无剧集时nextup为空 |

列表参数：ParentId、IncludeItemTypes、Recursive、SortBy、SortOrder、StartIndex、Limit、SearchTerm。SortBy允许DateCreated/SortName/PremiereDate，映射created_at/显示标题/release_date；多字段白名单解析，未知字段400，不拼SQL。SortOrder=Ascending/Descending，每次追加code为稳定tie-breaker。Limit默认50最大100，StartIndex非负且上限100000；深分页性能独立测试。空ParentId为根，Recursive=true且Movie过滤可平铺全影片；指定库按对应谓词筛选。

最新返回Item数组，列表/resume返回Items包；counts分别返回MovieCount、SeriesCount=0、EpisodeCount=0；nextup返回空Items包。G0若证明目标客户端需其他形状或别名，修改本契约和fixture后再实现，不能把新协议差异藏在业务查询中。

## 3. Movie / MediaSource映射

| DTO字段 | 来源 |
|---|---|
| Id/ServerId、Type=Movie、IsFolder=false | 稳定身份/固定类型 |
| Name、OriginalTitle | 显示名优先title_zh/official_title/title；OriginalTitle为官方原文，无则省略 |
| PremiereDate/ProductionYear | release_date，未知省略；不伪造为论坛发帖日 |
| Overview、People、Genres | 简介、规范演员、tags |
| CommunityRating | 0～5评分乘2，未知省略 |
| RunTimeTicks | 实际版本时长优先，影片资料时长次之；未知省略 |
| ImageTags.Primary、BackdropImageTags | 图片来源/内容版本hash，BackdropImageTags为数组 |
| UserData | PlaybackPositionTicks、Played、IsFavorite、PlayCount取当前用户进度 |
| MediaSources[].Id/Name | src_身份、版本显示名/大小/准备状态 |
| Container/Size/MediaStreams/RunTimeTicks | 经验证的物理信息；未知不假填mp4或固定时长 |
| Path/DirectStreamUrl/Protocol | 本服务版本流URL、Http；基址来自受信配置或受信反向代理 |
| SupportsDirectPlay | 仅对已证实容器/音视频能力匹配的source为true；未知先准备/识别 |
| SupportsDirectStream/SupportsTranscoding | 均false，首版不重封装/转码 |

PlaybackInfo返回{MediaSources,PlaySessionId}；显式版本检查归属，未指定按主设计实时选源。冷source可在详情展示并允许后台预准备；PlaybackInfo选中冷资源时先创建/复用持久准备任务，再返回503准备状态，用户完成后重试协商。不能伪造可解码信息。DeviceProfile不匹配时返回unsupported_media，不返回假转码URL。若上游无法提供媒体信息，G0必须证明目标客户端可自行探测或将该格式列受限；不能用unknown直接宣称可播。

## 4. 流与冷准备

成功仅302到就绪资产CDN；GET Range由目标CDN兑现206/Content-Range。HEAD使用同一身份校验但不新建任务；就绪时可返回302，无就绪时503。CDN UA/IP/额外headers约束必须G0验证，重定向本身不能传递任意下载头。

冷GET最多等待stream_wait_ms；超时503、Retry-After: 5、error.code=resource_preparing。管理端显示任务进度，用户完成后重新发起播放；第三方客户端可能只显示通用失败提示。首版不返回占位短片，不依靠自定义响应头自动循环。

显式版本失效不换版；默认版本可以在有限候选中回退。上游auth/429/超时不解绑资产。请求日志不得记录api_key或完整签名URL；302响应使用Cache-Control: no-store避免缓存陈旧跳转。

## 5. 会话与进度

PlaybackInfo建立negotiating会话，不更新user_progress；playing带ItemId/MediaSourceId/PlaySessionId/PositionTicks/IsPaused/DeviceId，服务端核对所有归属。若客户端省略PlaySessionId，只能在同用户同设备同影片同source恰有一个协商会话时关联，否则拒绝歧义；这条兼容行为必须EC-1证明。

playing激活会话并取得asset租约；progress续租更新；stopped首次关闭和可能增加完播计数，在同一事务内执行。100ns ticks，1秒=10000000；未知duration不除零/自动判完播。重复stopped不重复增加PlayCount。新实际会话替换进度写权限，旧会话迟到不覆盖新进度；同会话无客户端序号时按接收顺序，允许向后seek。

收藏/手动已看不增加播放次数。继续观看按played=0、position>0与last_played_at排序。未取得真流、准备失败和negotiating事件不能写正片进度。

## 6. 兼容矩阵

Infuse与VidHub分别记录：版本/系统/设备、token格式、路由大小写/前缀、views、PlaybackInfo、source切换、HEAD/Range、冷503界面、重试方式、心跳与长暂停、图片、续播。当前均待G0及P6正式服务实测。Kodi、Emby Web、其他浏览器只记录为扩展目标，不自动标通过。
