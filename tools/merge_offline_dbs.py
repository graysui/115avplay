# -*- coding: utf-8 -*-
"""离线磁力数据库合并与去重工具 (Sehuatang + X1080X 全量库融合).

业务规则:
1. 彻底删除/过滤: 'VR 视频区', '欧美无码', '三级写真' 这三个分类下的所有内容及分类；
2. 保留但不补全/不刮削: 'FC2 / 素人' 和 '国产 / 国内成人' (标记 is_enriched = 2)；
3. 保留并待补全: '中文字幕', '亚洲有码', '亚洲无码', '4K原版' (标记 is_enriched = 0)；
4. 磁力按 40 位 InfoHash 唯一去重，番号规范化聚合。
"""

from __future__ import annotations

import csv
import os
import re
import sqlite3
import sys
import time
from pathlib import Path
from typing import Any, Dict, List, Set, Tuple

# 40位/32位十六进制 InfoHash 正则
_HASH_RE = re.compile(r"(?i)urn:btih:([0-9a-fA-F]{32,40})")

# 彻底丢弃的分类集合 (包括所有变体)
EXCLUDED_SECTIONS = {
    "VR视频区",
    "VR",
    "VR 视频区",
    "欧美无码",
    "欧美",
    "三级写真",
    "写真",
}

# 免补全/免刮削的分类 (保留但跳过)
SKIP_ENRICH_SECTIONS = {
    "FC2/素人",
    "国产",
}

# 板块标准化映射
SECTION_MAPPING = {
    "中文字幕": "中文字幕",
    "高清中文字幕": "中文字幕",
    "亚洲有码": "亚洲有码",
    "亚洲无码": "亚洲无码",
    "4K原版": "4K原版",
    "4K": "4K原版",
    "FC2": "FC2/素人",
    "素人有码": "FC2/素人",
    "素人": "FC2/素人",
    "MGS": "FC2/素人",
    "国内成人": "国产",
    "国产": "国产",
    "主播精选": "国产",
    "探花精选": "国产",
    "韩国主播": "国产",
    "动漫原创": "亚洲有码",
}


def normalize_code(raw_number: str) -> str:
    """标准化番号，如 ssis-123 -> SSIS-123, fc2-1234567 -> FC2-PPV-1234567."""
    s = (raw_number or "").strip().upper()
    s = s.replace(" ", "").replace("_", "-")
    fc2_match = re.match(r"^FC2(?:-?PPV)?-?(\d+)$", s, re.IGNORECASE)
    if fc2_match:
        return f"FC2-PPV-{fc2_match.group(1)}"
    return s


def extract_info_hash(magnet_url: str) -> str:
    """提取 40 位标准大写 InfoHash."""
    if not magnet_url:
        return ""
    m = _HASH_RE.search(magnet_url)
    if m:
        return m.group(1).upper()
    return ""


def normalize_section(raw_section: str, raw_title: str) -> str:
    """根据板块与标题推断规范化分类，若在排除列表中则返回 'EXCLUDED'."""
    sec = (raw_section or "").strip()
    if sec in EXCLUDED_SECTIONS:
        return "EXCLUDED"
    if sec in SECTION_MAPPING:
        return SECTION_MAPPING[sec]

    # 辅助判断
    title_upper = (raw_title or "").upper()
    if "VR" in title_upper or "三级" in title_upper or "写真" in title_upper:
        return "EXCLUDED"
    if any(k in title_upper for k in ["中文字幕", "-C", "_C", "中文"]):
        return "中文字幕"
    if "4K" in title_upper or "2160P" in title_upper:
        return "4K原版"
    if "FC2" in title_upper or "SIRO" in title_upper:
        return "FC2/素人"
    if "无码" in title_upper or "UNCENSORED" in title_upper:
        return "亚洲无码"
    return "亚洲有码"


def extract_quality_label(title: str) -> str:
    """从标题提取画质规格."""
    t = (title or "").upper()
    if any(k in t for k in ["4K", "2160P", "UHD"]):
        return "4K"
    if any(k in t for k in ["1080P", "FHD", "BD", "蓝光", "BLURAY"]):
        return "1080P"
    if any(k in t for k in ["720P", "HD"]):
        return "720P"
    return "1080P"


def inspect_csv_headers(filepath: str, name: str) -> List[str]:
    """读取并打印 CSV 表头."""
    with open(filepath, "r", encoding="utf-8-sig", errors="replace") as fp:
        reader = csv.reader(fp)
        headers = next(reader)
        print(f"\n[*] 数据源 [{name}] 表头字段 (共 {len(headers)} 列):")
        print(f"    {headers}")
        return headers


def merge_offline_csvs(
    csv_paths: List[Tuple[str, str]],
    output_sqlite_path: str,
) -> None:
    """读取多个离线 CSV，过滤排除板块、去重合并并输出至本地 SQLite 数据库."""
    os.makedirs(os.path.dirname(os.path.abspath(output_sqlite_path)), exist_ok=True)
    if os.path.exists(output_sqlite_path):
        os.remove(output_sqlite_path)

    conn = sqlite3.connect(output_sqlite_path)
    cur = conn.cursor()

    cur.execute("PRAGMA synchronous = OFF;")
    cur.execute("PRAGMA journal_mode = MEMORY;")
    cur.execute("PRAGMA cache_size = 200000;")

    # 1. 创建完整离线库表结构
    cur.execute("""
        CREATE TABLE IF NOT EXISTS offline_movies (
            code            TEXT PRIMARY KEY,          -- 唯一规范化番号
            title           TEXT,                      -- 原始论坛标题
            category        TEXT,                      -- 标准分类 (中文字幕/亚洲有码/亚洲无码/4K原版/FC2/素人/国产)
            publish_date    TEXT,                      -- 发布日期
            preview_images  TEXT,                      -- 逗号分隔预览图 URL
            source_websites TEXT,                      -- 来源站点 ('sehuatang', 'x1080x', 或 'sehuatang,x1080x')
            
            -- 元数据增强字段 (供后续 SakuraPlayer / JavDB 补全)
            title_zh        TEXT,                      -- 中文官方翻译标题
            description_zh  TEXT,                      -- 中文剧情简介
            cover_url       TEXT,                      -- 官方高清封面 URL
            poster_url      TEXT,                      -- 竖版封面 URL
            actors          TEXT DEFAULT '[]',         -- 演员列表 (JSON 数组)
            tags            TEXT DEFAULT '[]',         -- 标签题材 (JSON 数组)
            maker           TEXT,                      -- 制作片商 (S1, Moodyz 等)
            director        TEXT,                      -- 导演
            score           REAL DEFAULT 0.0,          -- 评分 (0.00 ~ 5.00)
            is_enriched     INTEGER DEFAULT 0          -- 0: 待补全, 1: 已补全, 2: 免补全(FC2/国产)
        );
    """)

    cur.execute("""
        CREATE TABLE IF NOT EXISTS offline_magnets (
            info_hash       TEXT PRIMARY KEY,          -- 40位大写 Hash 唯一去重
            movie_code      TEXT NOT NULL,             -- 关联番号
            magnet_url      TEXT NOT NULL,             -- 完整磁力链
            title           TEXT,                      -- 磁力原始标题
            size_mb         INTEGER DEFAULT 0,         -- 大小 (MB)
            section         TEXT,                      -- 所属分类
            quality_label   TEXT DEFAULT '1080P',      -- 画质 (4K, 1080P, 720P)
            website         TEXT,                      -- 来源站点
            publish_date    TEXT,                      -- 发布时间
            FOREIGN KEY(movie_code) REFERENCES offline_movies(code)
        );
    """)

    cur.execute("CREATE INDEX IF NOT EXISTS idx_off_mag_code ON offline_magnets(movie_code);")
    cur.execute("CREATE INDEX IF NOT EXISTS idx_off_mov_cat ON offline_movies(category);")
    cur.execute("CREATE INDEX IF NOT EXISTS idx_off_mov_enriched ON offline_movies(is_enriched);")

    # 统计数据
    stats = {
        "total_rows_read": 0,
        "dropped_excluded": 0,
        "dropped_invalid": 0,
        "duplicate_hashes": 0,
        "by_website": {},
        "category_counts": {},
    }

    movies_dict: Dict[str, Dict[str, Any]] = {}
    magnets_dict: Dict[str, Dict[str, Any]] = {}

    t0 = time.time()

    for site_name, path in csv_paths:
        if not os.path.exists(path):
            print(f"[-] 文件不存在: {path}")
            continue

        print(f"\n[*] 正在读取并处理 [{site_name}] 全量数据: {path} ...")
        site_count = 0
        site_dropped = 0

        with open(path, "r", encoding="utf-8-sig", errors="replace") as fp:
            reader = csv.DictReader(fp)
            for row in reader:
                stats["total_rows_read"] += 1
                site_count += 1

                raw_num = row.get("number", "").strip()
                magnet_url = row.get("magnet", "").strip()
                if not raw_num or not magnet_url:
                    stats["dropped_invalid"] += 1
                    continue

                title = row.get("title", "").strip()
                raw_sec = row.get("section", "").strip()
                sec = normalize_section(raw_sec, title)

                # 彻底排除指定板块 (VR 视频区、欧美无码、三级写真)
                if sec == "EXCLUDED":
                    stats["dropped_excluded"] += 1
                    site_dropped += 1
                    continue

                code = normalize_code(raw_num)
                info_hash = extract_info_hash(magnet_url)
                if not info_hash:
                    stats["dropped_invalid"] += 1
                    continue

                pub_date = row.get("publish_date", "").strip()
                preview = row.get("preview_images", "").strip()
                size_str = row.get("size", "0").strip()
                try:
                    size_mb = int(float(size_str))
                except Exception:
                    size_mb = 0

                # 判断是否属于免补全分类 (FC2/素人, 国产)
                init_enriched = 2 if sec in SKIP_ENRICH_SECTIONS else 0

                # 1. 聚合影片信息
                if code not in movies_dict:
                    movies_dict[code] = {
                        "code": code,
                        "title": title,
                        "category": sec,
                        "publish_date": pub_date,
                        "preview_images": set(p for p in preview.split(",") if p.startswith("http")),
                        "websites": {site_name},
                        "is_enriched": init_enriched,
                    }
                else:
                    m = movies_dict[code]
                    m["websites"].add(site_name)
                    # 中文字幕优先提升
                    if sec == "中文字幕" and m["category"] != "中文字幕":
                        m["category"] = "中文字幕"
                        m["is_enriched"] = 0
                    for p in preview.split(","):
                        if p.startswith("http"):
                            m["preview_images"].add(p)
                    if pub_date and (not m["publish_date"] or pub_date < m["publish_date"]):
                        m["publish_date"] = pub_date

                # 2. 磁力去重合并
                if info_hash in magnets_dict:
                    stats["duplicate_hashes"] += 1
                else:
                    magnets_dict[info_hash] = {
                        "info_hash": info_hash,
                        "movie_code": code,
                        "magnet_url": magnet_url,
                        "title": title,
                        "size_mb": size_mb,
                        "section": sec,
                        "quality_label": extract_quality_label(title),
                        "website": site_name,
                        "publish_date": pub_date,
                    }

        stats["by_website"][site_name] = site_count
        print(f"[+] [{site_name}] 处理完毕: 读取 {site_count} 条，排除(VR/欧美/写真) {site_dropped} 条")

    # 3. 统计各分类影片数
    for m in movies_dict.values():
        c = m["category"]
        stats["category_counts"][c] = stats["category_counts"].get(c, 0) + 1

    # 4. 批量写入 SQLite
    print(f"\n[*] 正在将聚合去重后的数据高速写入 SQLite 数据库: {output_sqlite_path} ...")
    write_t0 = time.time()

    cur.execute("BEGIN TRANSACTION;")
    movies_batch = [
        (
            m["code"],
            m["title"],
            m["category"],
            m["publish_date"],
            ",".join(sorted(list(m["preview_images"]))),
            ",".join(sorted(list(m["websites"]))),
            m["is_enriched"],
        )
        for m in movies_dict.values()
    ]
    cur.executemany("""
        INSERT INTO offline_movies (code, title, category, publish_date, preview_images, source_websites, is_enriched)
        VALUES (?, ?, ?, ?, ?, ?, ?);
    """, movies_batch)

    magnets_batch = [
        (
            mag["info_hash"],
            mag["movie_code"],
            mag["magnet_url"],
            mag["title"],
            mag["size_mb"],
            mag["section"],
            mag["quality_label"],
            mag["website"],
            mag["publish_date"],
        )
        for mag in magnets_dict.values()
    ]
    cur.executemany("""
        INSERT INTO offline_magnets (info_hash, movie_code, magnet_url, title, size_mb, section, quality_label, website, publish_date)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
    """, magnets_batch)

    conn.commit()
    conn.close()

    total_time = time.time() - t0
    db_size_mb = round(os.path.getsize(output_sqlite_path) / (1024 * 1024), 2)

    # 5. 打印清晰汇总报告
    print("\n" + "=" * 60)
    print("      Sehuatang + X1080X 全量离线库合并与过滤报告")
    print("=" * 60)
    for site, count in stats["by_website"].items():
        print(f" - 数据源 [{site}] 原始记录: {count:,} 行")
    print(f" - 累计读取原始行数: {stats['total_rows_read']:,}")
    print(f" - 彻底删除排除板块 (VR视频区/欧美无码/三级写真): {stats['dropped_excluded']:,} 行")
    print(f" - 跨源/源内重复 InfoHash 过滤数: {stats['duplicate_hashes']:,}")
    print(f" - 最终入库唯一有效磁力数: {len(magnets_dict):,}")
    print(f" - 最终入库独立番号影片数: {len(movies_dict):,}")
    print("\n[+] 最终入库分类分布:")
    for cat, count in sorted(stats["category_counts"].items(), key=lambda x: -x[1]):
        enrich_note = "(免刮削/免补全)" if cat in SKIP_ENRICH_SECTIONS else "(待元数据补全)"
        print(f"    * {cat:10s}: {count:7,d} 部 {enrich_note}")
    print(f"\n[+] 本地完整离线数据库: {output_sqlite_path}")
    print(f"    - 文件大小: {db_size_mb} MB")
    print(f"    - 总耗时: {total_time:.2f} 秒")
    print("=" * 60)


def main() -> None:
    base_dir = Path(__file__).resolve().parent.parent
    raw_dir = base_dir / "data" / "raw_csv"

    full_sht = raw_dir / "All_sehuatang_full.csv"
    full_x1080 = raw_dir / "All_X1080X_full.csv"
    output_db = base_dir / "data" / "offline_full_merged.db"

    if not full_sht.exists() or not full_x1080.exists():
        print(f"[-] 缺少全量数据文件，请先运行 tools/download_full_dbs.py 下载解密")
        sys.exit(1)

    print("=======================================================")
    print("  离线数据库全量合并工具: Sehuatang & X1080X")
    print("  过滤规则: 剔除 VR/欧美/三级写真 | 保留 FC2/国产(免补全)")
    print("=======================================================")

    inspect_csv_headers(str(full_sht), "All_sehuatang")
    inspect_csv_headers(str(full_x1080), "All_X1080X")

    merge_offline_csvs(
        [("sehuatang", str(full_sht)), ("x1080x", str(full_x1080))],
        str(output_db),
    )


if __name__ == "__main__":
    main()
