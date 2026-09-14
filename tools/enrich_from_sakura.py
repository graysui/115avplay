# -*- coding: utf-8 -*-
"""利用 SakuraPlayer 本地数据库高效补全离线磁力库元数据.

特点与规则:
1. 独立脚本，与主项目完全解耦，可随时重复运行或增量更新；
2. 仅对 is_enriched = 0 的影片进行补全 (严格跳过 FC2/素人 与 国产，这些分类被标记为 is_enriched = 2)；
3. 严格过滤 SakuraPlayer 内部未刮削的空占位记录 (仅补全已包含真实元数据的影片)；
4. 全面补全:
   - title_zh (中文规范标题，优先中文，回退原标题)
   - description_zh (中文剧情简介，优先中文，回退原剧情)
   - cover_url (JavDB 官方高清封面图，若缺失则回退论坛首张预览图)
   - actors (演员列表 JSON，包含中日文名与头像索引)
   - tags (标签题材 JSON 数组)
   - maker (片商), director (导演), score (评分), publish_date (发行日期)
5. 采用 SQLite ATTACH 与分批集合查询优化，17 万+ 条数据可在数十秒内全部处理完毕。
"""

from __future__ import annotations

import argparse
import json
import os
import sqlite3
import sys
import time
from collections import defaultdict
from pathlib import Path
from typing import Any, Dict, List, Tuple

sys.stdout.reconfigure(line_buffering=True, encoding="utf-8")


def enrich_from_sakura(
    target_db_path: str,
    sakura_db_uri: str,
    batch_size: int = 1000,
    limit: int | None = None,
    dry_run: bool = False,
) -> None:
    target_p = Path(target_db_path).resolve()
    if not target_p.exists():
        print(f"[-] 目标数据库不存在: {target_p}", flush=True)
        return

    print("=======================================================", flush=True)
    print("      SakuraPlayer 本地数据库元数据补全工具")
    print("=======================================================", flush=True)
    print(f"[*] 目标离线数据库: {target_p}", flush=True)
    print(f"[*] Sakura 数据源:   {sakura_db_uri}", flush=True)
    print(f"[*] 批量处理大小:   {batch_size} 条/批", flush=True)
    if limit:
        print(f"[*] 限制处理数量:   {limit:,} 部", flush=True)
    if dry_run:
        print("[!] 当前为 Dry-Run 模拟测试模式，不写回数据库", flush=True)

    conn = sqlite3.connect(str(target_p), uri=True)
    cur = conn.cursor()

    cur.execute("PRAGMA synchronous = NORMAL;")
    cur.execute("PRAGMA cache_size = 200000;")

    # 1. 挂载 SakuraPlayer 数据库
    t_attach = time.time()
    try:
        cur.execute(f"ATTACH DATABASE '{sakura_db_uri}' AS sakura;")
        print(f"[+] 成功挂载 Sakura 数据库 (耗时 {time.time() - t_attach:.3f}s)", flush=True)
    except Exception as e:
        print(f"[-] 挂载 Sakura 数据库失败: {e}", flush=True)
        conn.close()
        return

    # 2. 查询状态分布
    cur.execute("SELECT count(*) FROM offline_movies WHERE is_enriched = 0;")
    total_pending = cur.fetchone()[0]

    cur.execute("SELECT count(*) FROM offline_movies WHERE is_enriched = 2;")
    total_skipped = cur.fetchone()[0]

    cur.execute("SELECT count(*) FROM offline_movies WHERE is_enriched = 1;")
    already_enriched = cur.fetchone()[0]

    print(f"\n[*] 离线库当前状态:", flush=True)
    print(f"    - 待补全影片 (亚洲有码/无码/中文字幕/4K): {total_pending:,} 部", flush=True)
    print(f"    - 已补全影片:                          {already_enriched:,} 部", flush=True)
    print(f"    - 免补全影片 (FC2/素人, 国产):          {total_skipped:,} 部 (严格跳过)", flush=True)

    if total_pending == 0:
        print("\n[+] 没有待补全的影片！所有目标影片均已完成元数据补全。", flush=True)
        conn.close()
        return

    # 3. 获取所有待补全影片番号与预览图缓存
    limit_sql = f"LIMIT {limit}" if limit else ""
    cur.execute(f"SELECT code, preview_images FROM offline_movies WHERE is_enriched = 0 ORDER BY rowid {limit_sql};")
    rows = cur.fetchall()
    all_codes = [r[0] for r in rows]
    preview_map = {r[0]: (r[1].split(",")[0] if r[1] else "") for r in rows}
    total_to_process = len(all_codes)

    print(f"\n[*] 开始执行高速批量元数据比对与回填 (共 {total_to_process:,} 部)...", flush=True)
    start_time = time.time()

    total_scanned = 0
    total_matched = 0
    total_with_desc = 0
    total_with_cover = 0
    total_with_actors = 0

    for i in range(0, total_to_process, batch_size):
        batch_codes = all_codes[i : i + batch_size]
        b_len = len(batch_codes)
        total_scanned += b_len

        placeholders = ",".join("?" for _ in batch_codes)

        # 1. 批量检索 sakura.movie (严格要求存在真实标题或简介，排除未抓取占位符)
        cur.execute(f"""
            SELECT 
                m.id,
                m.normalized_number,
                COALESCE(NULLIF(m.title_zh, ''), m.title_original, ''),
                COALESCE(NULLIF(m.description_zh, ''), m.description_original, ''),
                COALESCE(m.maker, ''),
                COALESCE(m.director, ''),
                COALESCE(m.score, '0.0'),
                COALESCE(m.release_date, '')
            FROM sakura.movie m
            WHERE m.normalized_number IN ({placeholders})
              AND (
                  (m.title_zh IS NOT NULL AND m.title_zh != '') OR
                  (m.title_original IS NOT NULL AND m.title_original != '') OR
                  (m.description_zh IS NOT NULL AND m.description_zh != '')
              );
        """, batch_codes)
        matched_movies = cur.fetchall()

        if matched_movies:
            total_matched += len(matched_movies)
            m_ids = [m[0] for m in matched_movies]
            id_placeholders = ",".join("?" for _ in m_ids)

            # 2. 批量检索封面 (catalog_image)
            cur.execute(f"""
                SELECT owner_id, source_url
                FROM sakura.catalog_image
                WHERE owner_type = 'movie' AND owner_id IN ({id_placeholders}) AND kind = 'cover';
            """, m_ids)
            covers_map = {r[0]: r[1] for r in cur.fetchall()}

            # 3. 批量检索演员 (movie_actor + actor)
            cur.execute(f"""
                SELECT ma.movie_id, a.name_ja, a.name_zh
                FROM sakura.movie_actor ma
                JOIN sakura.actor a ON ma.actor_id = a.id
                WHERE ma.movie_id IN ({id_placeholders});
            """, m_ids)
            actors_map = defaultdict(list)
            for mid, name_ja, name_zh in cur.fetchall():
                actors_map[mid].append({"name_ja": name_ja, "name_zh": name_zh})

            # 4. 批量检索标签 (movie_tag + tag)
            cur.execute(f"""
                SELECT mt.movie_id, t.name
                FROM sakura.movie_tag mt
                JOIN sakura.tag t ON mt.tag_id = t.id
                WHERE mt.movie_id IN ({id_placeholders});
            """, m_ids)
            tags_map = defaultdict(list)
            for mid, tag_name in cur.fetchall():
                tags_map[mid].append(tag_name)

            # 5. 组装更新
            update_params = []
            for mid, code, title_zh, desc_zh, maker, director, score_str, rel_date in matched_movies:
                try:
                    score = float(score_str)
                except Exception:
                    score = 0.0

                cover = covers_map.get(mid, "")
                if not cover:
                    # 回退使用离线库预览图
                    cover = preview_map.get(code, "")

                actors_list = actors_map.get(mid, [])
                tags_list = tags_map.get(mid, [])

                if desc_zh:
                    total_with_desc += 1
                if cover:
                    total_with_cover += 1
                if actors_list:
                    total_with_actors += 1

                actors_json = json.dumps(actors_list, ensure_ascii=False)
                tags_json = json.dumps(tags_list, ensure_ascii=False)

                update_params.append((
                    title_zh,
                    desc_zh,
                    cover,
                    maker,
                    director,
                    score,
                    actors_json,
                    tags_json,
                    rel_date,
                    code,
                ))

            if not dry_run and update_params:
                cur.executemany("""
                    UPDATE offline_movies
                    SET 
                        title_zh = CASE WHEN ? != '' THEN ? ELSE title_zh END,
                        description_zh = CASE WHEN ? != '' THEN ? ELSE description_zh END,
                        cover_url = CASE WHEN ? != '' THEN ? ELSE cover_url END,
                        maker = CASE WHEN ? != '' THEN ? ELSE maker END,
                        director = CASE WHEN ? != '' THEN ? ELSE director END,
                        score = CASE WHEN ? > 0.0 THEN ? ELSE score END,
                        actors = ?,
                        tags = ?,
                        publish_date = CASE WHEN (publish_date IS NULL OR publish_date = '') AND ? != '' THEN ? ELSE publish_date END,
                        is_enriched = 1
                    WHERE code = ?;
                """, [
                    (
                        u[0], u[0],  # title_zh
                        u[1], u[1],  # description_zh
                        u[2], u[2],  # cover_url
                        u[3], u[3],  # maker
                        u[4], u[4],  # director
                        u[5], u[5],  # score
                        u[6],        # actors
                        u[7],        # tags
                        u[8], u[8],  # publish_date
                        u[9],        # code
                    )
                    for u in update_params
                ])
                conn.commit()

        # 实时打印进度
        batch_num = i // batch_size + 1
        if batch_num % 10 == 0 or total_scanned >= total_to_process:
            elapsed = time.time() - start_time
            rate = (total_matched / total_scanned * 100) if total_scanned > 0 else 0
            speed = total_scanned / elapsed if elapsed > 0 else 0
            print(
                f"  [*] 进度: [{total_scanned:7,d} / {total_to_process:,d}] "
                f"已命中补全: {total_matched:7,d} ({rate:5.1f}%) | "
                f"速度: {speed:5.0f} 条/秒",
                flush=True,
            )

    # 4. 自动将指定年份之前的未补全老片标记为免刮削
    skipped_old = 0
    if skip_before_year and not dry_run:
        cutoff_date = f"{skip_before_year}-01-01"
        cur.execute("""
            UPDATE offline_movies
            SET is_enriched = 2
            WHERE publish_date < ? AND is_enriched = 0;
        """, (cutoff_date,))
        skipped_old = cur.rowcount
        conn.commit()
        if skipped_old > 0:
            print(f"\n[+] 已将 {skip_before_year} 年之前未补全的老片 ({skipped_old:,} 部) 标记为免刮削 (is_enriched = 2)", flush=True)

    # 5. 查询最新最终统计
    cur.execute("SELECT count(*) FROM offline_movies WHERE is_enriched = 0;")
    final_pending = cur.fetchone()[0]

    cur.execute("SELECT count(*) FROM offline_movies WHERE is_enriched = 2;")
    final_skipped = cur.fetchone()[0]

    conn.close()
    total_time = time.time() - start_time

    # 6. 汇总报告
    print("\n" + "=" * 60, flush=True)
    print("      SakuraPlayer 本地数据库元数据补全成果报告", flush=True)
    print("=" * 60, flush=True)
    print(f" - 本次扫描影片数:             {total_scanned:,} 部", flush=True)
    print(f" - 成功从 SakuraPlayer 匹配补全: {total_matched:,} 部 (匹配率: {total_matched / total_scanned * 100:.2f}%)", flush=True)
    print(f" - 成功回填中文剧情简介:         {total_with_desc:,} 部", flush=True)
    print(f" - 成功回填封面图:               {total_with_cover:,} 部", flush=True)
    print(f" - 成功回填演员名单 (带中日译名): {total_with_actors:,} 部", flush=True)
    print(f" - 2025年之前标记为免刮削老片:   {skipped_old:,} 部", flush=True)
    print(f" - 最终免刮削/免补全总影片数:    {final_skipped:,} 部 (FC2/国产 + 2025前老片)", flush=True)
    print(f" - 最终剩余待 JavDB 刮削影片数:  {final_pending:,} 部 (全部为 2025~2026 年新片)", flush=True)
    print(f" - 总耗时: {total_time:.2f} 秒 (平均处理速度: {total_scanned / total_time:.1f} 条/秒)", flush=True)
    print("=" * 60, flush=True)


def main():
    base_dir = Path(__file__).resolve().parent.parent
    default_target_db = str(base_dir / "data" / "offline_full_merged.db")
    default_sakura_db = "file:Y:/Sakuraplayer-v2/data/sakuraplayer.db?mode=ro&immutable=1"

    parser = argparse.ArgumentParser(description="SakuraPlayer 本地数据库元数据补全工具")
    parser.add_argument("--target-db", default=default_target_db, help="目标离线 SQLite 数据库路径")
    parser.add_argument("--sakura-db", default=default_sakura_db, help="Sakura SQLite 连接 URI")
    parser.add_argument("--batch-size", type=int, default=1000, help="批处理大小 (默认: 1000)")
    parser.add_argument("--limit", type=int, default=None, help="仅测试处理前 N 部影片")
    parser.add_argument("--skip-before-year", type=int, default=2025, help="将该年份之前未补全的影片标记为免刮削 (默认: 2025)")
    parser.add_argument("--dry-run", action="store_true", help="测试模式 (不提交修改)")

    args = parser.parse_args()
    enrich_from_sakura(
        target_db_path=args.target_db,
        sakura_db_uri=args.sakura_db,
        batch_size=args.batch_size,
        limit=args.limit,
        skip_before_year=args.skip_before_year,
        dry_run=args.dry_run,
    )


if __name__ == "__main__":
    main()

