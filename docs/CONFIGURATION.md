# 计划配置合同

对应主设计§3、§8、§9.3、§12。P0/P1实现默认值及类型校验，P7实现编辑页面。本文件描述将来的Go服务配置，不表示旧Python工具已经读取这些设置。

## 启动变量

| 名称 | 规则 |
|---|---|
| MV_DATA_DIR | 默认./data，启动时解析为绝对路径，数据库/日志/缓存置于其中 |
| MV_MASTER_KEY_FILE | 独立密钥文件，Base64编码32随机字节；已有密文而缺失/不匹配时启动失败 |
| MV_ADMIN_PASSWORD | 首次初始化可指定；既有用户密码不被环境变量重置。未设置按主设计创建本地一次性密码文件 |
| MV_LISTEN | 默认127.0.0.1:8096；对外部署须显式配置 |
| MV_PUBLIC_URL | 客户端可访问的固定HTTP(S)基址，可含部署前缀；生产反向代理场景必须设置 |
| MV_TRUSTED_PROXIES | 默认空；只有明确CIDR代理可影响Forwarded/X-Forwarded-*，否则忽略 |
| MV_<SETTING_KEY大写> | 普通设置的环境覆盖；优先于DB，UI标只读；不以此机制接收secret明文 |
| MV_LOG_LEVEL | DEBUG/INFO/WARN/ERROR，默认INFO |

MV_PUBLIC_URL未设置时直接监听部署可按请求Host生成同源相对地址；不得根据未受信转发头生成外部URL。保留前缀时用URL join而非字符串拼接，登录/图片/流URL通过同一规则生成。

## 设置表：类型、默认与范围

| key | 类型/默认 | 验证与生效 |
|---|---|---|
| temp_transfer_cid | string/空 | 空时拒绝新转存；不能为0、不能与任一永久根相同或祖先/后代；未清理资产存在时拒绝更改 |
| existing_scan_cids | JSON string[]/[] | CID字符串去重，验证目录；新增根必须与临时根不重叠 |
| existing_scan_interval_min | int/360 | 15～1440，增量扫描周期；下个日程生效 |
| full_scan_interval_hours | int/24 | 1～24，完整对账周期 |
| cleanup_enabled | bool/true | 停止新删除提交，不改变TTL可播资格；已提交删除继续确认 |
| cleanup_ttl_days | int/7 | 1～30；只影响新ready资产的快照 |
| cleanup_confirm_timeout_sec | int/600 | 30～3600；超时保留reconcile身份 |
| playback_lease_sec | int/120 | 60～600；应配合已验证客户端心跳，长暂停限制须呈现 |
| transfer_concurrency | int/2 | 1～4，新任务领取时使用；降低不取消在途任务 |
| transfer_deadline_sec | int/7200 | 300～86400；新任务快照 |
| stream_wait_ms | int/8000 | 0～10000；不超过已验证客户端请求时限 |
| scrape_concurrency | int/2 | 1～2；所有JavDB任务共享 |
| scrape_delay_min/max | float/2.5、4.5 | 0～60且min<=max；除令牌桶外的随机延迟 |
| javdb_request_interval_sec | float/3 | >=3且burst=1，共享全局桶 |
| javdb_search_timeout_ms | int/6000 | 1000～10000，包含排队和全链路请求 |
| javdb_partial_attempts | int/5 | 1～10，每日补全预算 |
| magnet_refresh_hours | int/24 | >=24，已有影片资源核对间隔 |
| proxy_url | string/空 | 仅HTTP(S)/SOCKS5受支持协议；若含认证信息则secret，不得返回明文 |
| image_proxy | bool/true | 关闭须显示来源直链限制 |
| image_cache_bytes | int/2147483648 | >=104857600，LRU超过上限清到80% |
| image_max_bytes | int/10485760 | 1MiB～20MiB，单图上限 |
| download_cache_bytes | int/2147483648 | 最近两次完整release且受容量上限约束，活动下载不被裁剪 |
| archive_max_bytes | int/536870912 | 单包下载上限，超限停止 |
| archive_expanded_max_bytes | int/4294967296 | 解压总量上限，执行前预检空间 |
| pbkdf2_max_iterations | int/1000000 | >=200000，拒绝清单异常成本 |
| log_retention_days | int/7 | 1～30 |
| log_max_bytes | int/268435456 | 日志总量上限 |
| job_retention_days | int/30 | 7～365；不能删除未完成或仍被资产/扫描/摄取记录引用的任务 |
| job_history_limit | int/100000 | 已完成且可安全裁剪记录上限；受引用保护者可超额并告警 |
| queue_limit | int/1000 | 1～10000；满时429，已存在任务查询不受影响 |
| disk_min_free_bytes | int/1073741824 | 新下载/导入门槛；迁移还需备份/新表/WAL额外空间 |
| alert_webhook | string/空 | HTTP(S)，完整URL视为secret；投递超时5秒、不记录签名参数 |
| alert_silence_sec | int/3600 | 60～86400 |
| sync_watermark | 内部JSON/空 | 用户只读，仅两来源覆盖成功事务推进 |
| provider_tokens:<binding_id> | secret JSON | 内部OAuth原子更新，管理GET只有configured标志 |

对外PUT采用{revision,values,secrets}；普通values做完整类型/范围验证后原子提交。secrets逐项为{action:keep}、{action:replace,value:...}或{action:clear}；不能同时带无关字段。环境覆盖项不可修改；revision不匹配409。配置校验失败不部分保存。

每个jobs.params_json记录不含明文secret的配置revision、普通参数快照与secret引用；执行时按secret引用使用最新有效令牌，不能用已轮换的过期token快照。代理切换/并发/TTL规则应用于新任务；凭据续期例外必须即时可用。

## 日程

schedules使用IANA时区，默认Asia/Shanghai。rule_json仅允许daily{hour,minute}或weekly{weekday,hour,minute}，weekday=1～7；rankings的params含board/year；sync30d默认每天04:00；scan以配置周期生成slot。

以schedule_id+计划UTC触发时间生成slot幂等键，持久last_slot/next_run_at。停机错过多个slot只补最近一次，并另做覆盖缺口检查；不连发所有遗漏任务。夏令时不存在时刻顺延到下一有效分钟，重复时刻执行一次。日程修改从下一计划点生效，禁止任意cron字符串未经校验直接执行。

首次未授权时仍可编辑日程；扫描/转存任务返回明确not_configured状态。授权或根修改与现有资产冲突必须在页面列出原因和可操作清理方式，不能悄悄换绑后删除旧账号文件。
