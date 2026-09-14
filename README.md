# 115AVPlay / MediaVault

面向 115 的 Go 虚拟媒体服务器设计与数据工具仓库。目标是以 SQLite 投影媒体目录，通过 Emby 协议向 Infuse/VidHub 提供浏览、多版本和续播，通过短时 302 直连就绪视频。

**当前状态：设计与实施准备阶段，Go 服务和管理前端尚未交付。** 仓库里的 Python 工具及历史提取代码仅为数据处理和实现参考，不能作为可部署服务器启动。

## 阅读顺序

1. [功能规格](specs/mediavault-server/spec.md)：范围和24个验收场景。
2. [技术设计](docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md)：状态、资源、权限与播放行为。
3. [目标数据库](docs/database_schema.sql)与[迁移合同](docs/DATABASE_MIGRATION.md)：仅对副本演练，DDL不是旧库升级脚本。
4. [实施计划](specs/mediavault-server/plan.md)与[需求追踪](specs/mediavault-server/traceability.md)。
5. [外部验证门槛](specs/mediavault-server/phases/00-integration-gate.md)：115/JavDB/客户端当前能力仍需真实验证。

## 运行边界

计划首版仅DirectPlay，不做转码、重封装、ISO或分段拼接。冷资源可在管理端预准备；播放等待超时会提示准备状态，完成后用户手动重试，不依赖客户端自动重试。

data/ 内数据库、下载和缓存不上传。克隆仓库不会取得本地数据集；正式服务计划支持挂载旧库升级或空库全量导入。旧Python写库工具只用于相应旧格式副本，不应直接作用于未来迁移后的运行库。

未来部署使用独立主密钥文件、管理员初始化及115授权；确切构建命令、配置清单和Docker产物由P8在实现完成后发布。当前不要把计划中的接口路径当成已经运行的服务。
