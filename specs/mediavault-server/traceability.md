# 需求—设计—任务—验收追踪

本表是实施导航，不是完成记录。技术章节均指[主设计](../../docs/PROJECT_SPEC_GO_VIRTUAL_MEDIA_SERVER.md)，任务见[阶段目录](./phases/)，验收见[需求](./spec.md)。当前任务均未开始；G0的真实外部验证与P8正式服务验收分别留证。

| 需求 | 设计 | 任务 | 验收 |
|---|---|---|---|
| FR-DATA-1 | §4；DDL | T-101 T-102 T-103 | AC-1 AC-11 |
| FR-DATA-2 | §4.4；迁移规范 | T-107 T-108 | AC-11 |
| FR-DATA-3 | §7.1 | T-102 T-106 T-407 | AC-16 |
| FR-DATA-4 | §5.4；身份样例 | T-109 T-305 | AC-17 |
| FR-INGEST-1 | §5.1/5.3 | T-301 T-302 T-303 T-309 | AC-2 AC-18 |
| FR-INGEST-2 | §5.2/6 | T-304 T-409 | AC-2 AC-3 |
| FR-INGEST-3 | §5.2 | T-306 T-405 | AC-2 AC-18 |
| FR-INGEST-4 | §5.1；配置日程 | T-307 T-308 | AC-18 AC-24 |
| FR-SCRAPE-1 | §6 | T-404 T-405 | AC-3 |
| FR-SCRAPE-2 | §6 | T-404 T-411 T-412 | AC-3 |
| FR-SCRAPE-3 | §6 | T-404 T-412 | AC-3 |
| FR-SCRAPE-4 | §7.1/7.2 | T-110 T-406 T-407 | AC-16 |
| FR-SCRAPE-5 | §5.2/6 | T-408 T-409 T-706 | AC-4 AC-24 |
| FR-SCRAPE-6 | §6 | T-401 T-412 | AC-3 AC-23 |
| FR-STREAM-1 | §8.3；扫描规范 | T-204 T-502 | AC-5 AC-19 |
| FR-STREAM-2 | §8.1 | T-206 T-503 | AC-6 AC-13 |
| FR-STREAM-3 | §8.4 | T-201 T-205 T-505 | AC-7 |
| FR-STREAM-4 | §1/8.4 | T-506 T-704 T-613 | AC-6 |
| FR-STREAM-5 | §8.2 | T-207 T-504 T-507 | AC-8 AC-14 AC-15 |
| FR-STREAM-6 | §4.3/8.1 | T-111 T-503 T-508 | AC-12 AC-13 |
| FR-EMBY-1 | §9.1；协议参考 | T-601 T-602 T-604 T-608 T-613 | AC-5 AC-9 |
| FR-EMBY-2 | §7.3/9.1 | T-605 T-606 | AC-4 AC-5 |
| FR-EMBY-3 | §9.1 | T-608 T-610 | AC-12 |
| FR-EMBY-4 | §9.2 | T-609 | AC-5 AC-23 |
| FR-EMBY-5 | §8.4/9.1 | T-205 T-610 T-613 | AC-5 AC-6 |
| FR-EMBY-6 | §9.4 | T-611 T-612 | AC-9 AC-20 |
| FR-EMBY-7 | §7.4 | T-402 T-410 T-607 T-806 | AC-4 |
| FR-ADMIN-1 | §10.1 | T-702 | AC-3 |
| FR-ADMIN-2 | §5.2/8.2/10.1 | T-507 T-704 | AC-14 AC-16 |
| FR-ADMIN-3 | §6/10.1 | T-411 T-705 | AC-3 AC-24 |
| FR-ADMIN-4 | §10.1；配置日程 | T-506 T-704 T-706 | AC-6 AC-18 AC-24 |
| FR-ADMIN-5 | §10.1 | T-004 T-707 T-708 | AC-21 AC-24 |
| FR-ADMIN-6 | §9.3/10.1；配置规范 | T-202 T-703 T-704 T-707 T-712 | AC-22 AC-24 |
| FR-ADMIN-7 | §9.3 | T-603 T-701 T-712 T-713 | AC-21 |
| FR-OPS-1 | §10.2 | T-007 T-801 | AC-1 AC-10 |
| FR-OPS-2 | §10.2 | T-707 T-802 T-803 | AC-10 AC-24 |
| FR-OPS-3 | §12；配置规范 | T-004 T-609 T-804 | AC-23 AC-24 |
| FR-OPS-4 | §4.3/8.1 | T-111 T-308 T-503 T-508 T-805 | AC-13 AC-14 AC-18 |
| NFR-1 | §3/12 | T-001 T-008 T-711 T-807 | AC-23 |
| NFR-2 | §12 | T-005 T-106 T-809 | AC-23 |
| NFR-3 | §12 | T-505 T-809 | AC-5 AC-23 |
| NFR-4 | §6 | T-401 T-410 T-412 | AC-3 AC-23 |
| NFR-5 | §3/8/12 | T-009 T-201 T-208 T-503 | AC-7 AC-13 |
| NFR-6 | §4.4；迁移规范 | T-006 T-107 T-108 T-805 | AC-11 |
| NFR-7 | §9.3 | T-003 T-004 T-603 T-701 T-810 | AC-21 AC-22 |

## 用户故事与范围

| 故事 | 对应需求 |
|---|---|
| US-1 | FR-EMBY-2、FR-EMBY-4 |
| US-2 | FR-EMBY-7 |
| US-3 | FR-STREAM-2、FR-STREAM-4 |
| US-4 | FR-EMBY-3、FR-STREAM-6 |
| US-5 | FR-EMBY-6 |
| US-6 | FR-INGEST-1、FR-INGEST-4、FR-SCRAPE-5 |
| US-7 | FR-ADMIN-3、FR-SCRAPE-1～3 |
| US-8 | FR-ADMIN-6、FR-STREAM-5 |
| US-9 | FR-OPS-2 |
| US-10 | FR-OPS-4、FR-STREAM-5 |

## 验证责任

G0：T-G01～T-G06取得115/JavDB和两客户端真实证据；P1负责AC-11/16/17的数据/算法基础；P5负责资产恢复和清理；P6负责协议与进度；P7负责后台入口；P8的T-806执行全部AC-1～24并记录通过/失败/复验。其他阶段可以使用接口桩完成局部测试，但不能替代P8的完整业务结果。

配置字段清单见[配置规范](../../docs/CONFIGURATION.md)；旧库字段转换见[迁移规范](../../docs/DATABASE_MIGRATION.md)。候选外部URL只有通过G0并补fixture后才是已验证契约。
