# 既有离线库迁移与恢复

本文是 P1 必须实现和演练的迁移合同。目标结构以 [database_schema.sql](./database_schema.sql) 为准，不能把目标建库 SQL 直接用于旧库。当前文档修订没有修改 data/ 中的数据库。

## 1. 基线与版本识别

2026-09-14 对本地旧库的只读核对：2 张业务表；212,034 部影片、326,808 条资源；326,808 条 is_available=1 却无 pick_code；18,239 条 is_enriched=1 却缺简介；资源外键为 NO ACTION；resource_kind、若干时间列及新辅助表缺失。数量仅用于这份样本的回归，不作为其他用户库的硬编码校验。

启动先通过 sqlite_master、table_info、foreign_key_list、index_list 识别形状。没有 schema_meta 的这份形状记为 legacy-v0；不识别的形状拒绝自动升级，给出差异列表。目标 schema_version=1；运行时版本高于程序认识范围则只报错，不允许旧程序写新库。DDL 在空库首次应用一次，重复启动通过版本/迁移校验和跳过，不在每次启动无条件重放 CREATE TABLE。

## 2. 备份及事务边界

1. 停止服务和所有旧工具写者，取得跨进程独占文件锁。禁止在线将旧 Python 工具与新服务同时指向运行库。
2. 使用 SQLite 在线备份 API 在锁定快照下生成备份，或关闭所有连接并成功 checkpoint 后复制数据库；不能只复制还带未合并 WAL 的主文件。记录数据库和备份 SHA-256、主键清单、表数量、关键字段摘要。备份旁保存密钥版本，不在同一公开目录存主密钥。
3. 预检磁盘空间至少满足备份、临时新表及 WAL 的估算量；不满足就退出且不修改原库。P1 输出具体估算值。
4. 专用迁移连接在事务外设置 foreign_keys=OFF，然后 BEGIN IMMEDIATE；创建目标新表临时名或先将旧表复制到迁移专用临时表。所有数据搬运、删除旧表、改名、索引/触发器创建和版本更新在一个事务内完成。正式脚本必须明确外键最终引用的目标名，避免 ALTER RENAME 把外键永久指向旧名。
5. 列映射复制后，检查主键集合、行数、逐字段摘要、foreign_key_check 和 quick_check，全部通过才写 schema_version、迁移校验和及 server_id 并 COMMIT。退出时重新启用 foreign_keys 并再检查。
6. 异常/断电由事务回滚恢复升级前状态；重启重新识别版本。失败不得留下“版本已升级、结构未完成”的数据库。

允许事务内受控重建约束，不允许丢掉业务行来满足约束。若碰到不合法字段，保留原始内容到 legacy_metadata/legacy_runtime 和迁移报告，必要时整体中止等待明确映射；不得悄悄丢行。

## 3. 字段与状态映射

| 旧资产 | 目标处理 |
|---|---|
| code / info_hash 主键 | 保留；本地样本均为 btih。非本样本的合成 key 需生成旧→新映射并同步所有引用 |
| title/category/publish_date、原元数据 | 逐列保留；NULL category→未知，旧 category 原值另存 legacy_metadata；不把发帖日期改为发行日 |
| source_websites 逗号集合 | 拆成去重 JSON 数组，原字符串保留到 legacy_metadata |
| actors/tags | 验证 JSON；不合规则原文保留，规范列回空数组并记录异常 |
| score/未知时长 | 有效 0～5 保留，非法评分保留原文并置 NULL；无证据的时长保持 NULL |
| created_at/updated_at/first_seen_at | 已有合法时间转 UTC RFC3339；无旧时间用本次 migration_at，标 source=legacy_unknown；不能用发行日假装首次入库日 |
| 旧 is_enriched=1 | 按实际字段完整度计算 1 或 3，缺字段进入 auto；本地样本应至少把缺简介的 18,239 行转为 3 |
| 旧 is_enriched=0 | 根据已有字段计算 0/3，auto；保留旧抓取错误到 legacy_metadata |
| 旧 is_enriched=2 | 完整度独立重算；FC2/国产→exempt/category；其余→paused/legacy_skip，保留旧原因/年份信息供管理员恢复 |
| scrape_status/retry_count | 保存旧值到 legacy_metadata；新结果按最后可证实结果映射；新 not_found_count=0，不把不明旧失败当作三次明确 404 |
| resource_kind 缺失 | 解析规范磁力；本地样本→btih，content_hash=原 info_hash |
| size_mb | 兼容旧工具口径按 1048576 转整数 size_bytes，同时保存旧 size_mb；有可信实际文件字节数时优先实际值 |
| quality/priority/preferred | 标题重新提取属性，priority_score 由目标生成列计算；清空旧优选后按新分组规则选源；旧分数保留到 legacy_runtime |
| source_type/transfer_status/is_available/pick_code 等 | 全量保留到 legacy_runtime；不继承 is_available；有有效授权绑定且已确认的物理文件才建立 cloud_assets |
| 旧没有 pick_code 的资源 | 不创建 ready 资产；可转存资格由资源类型/链接决定，本地样本 326,808 条均不可立即播放 |
| 存在旧云文件却无法证明归属 | 无可信 binding 时只保留原始记录并创建人工核对提示；绑定明确后可建 quarantined 资产，不允许自动删除 |
| 缺失运行表 | 依 DDL 新建；库分类初始化；迁移不臆造用户/云账号/远端任务 |

NOT NULL/DEFAULT/CHECK/外键变化通过表重建实现。不能使用 SQLite 不支持的 ADD COLUMN IF NOT EXISTS；含数据表不能直接增加 DEFAULT CURRENT_TIMESTAMP 列。目标写入显式传 UTC 时间，不依赖新增列的非恒定默认值。

## 4. 恢复、降级与演练

事务未提交时回滚；已提交后程序降级使用升级前备份，原升级库另行保留，禁止反向删列“还原”。备份恢复必须同时匹配主密钥版本。迁移前后直接恢复测试及对旧数据工具的写入隔离，是 P1 的完成条件。

P1 在样本库副本上执行：空库建库；完整旧库升级；至少三个搬运阶段故障注入；重复升级；外键级联；资源删除后任务/资产 SET NULL 保留身份；错误密钥恢复失败；正确备份可恢复。比较全部主键及保留字段，不只比总数。实际通过记录由实现阶段生成，不把本规范当成通过报告。
