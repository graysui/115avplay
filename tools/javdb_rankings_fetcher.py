# -*- coding: utf-8 -*-
"""JavDB 周榜、月榜与 TOP 250 获取工具 (独立运行版).

依赖:
    pip install requests

功能:
    1. 获取日榜、周榜、月榜 (支持全部、有码、无码、欧美)
    2. 获取 TOP 250 历史总榜 (支持自动合并 1-250 全量榜单，或按年份/分类筛选)
    3. 支持终端彩色/表格预览及导出为 JSON / CSV

使用方式:
    # 1. 获取本周周榜 (默认全部)
    python tools/javdb-rankings/javdb_rankings_fetcher.py --ranking weekly

    # 2. 获取本月月榜 (仅有码)
    python tools/javdb-rankings/javdb_rankings_fetcher.py --ranking monthly --type 1

    # 3. 获取完整 TOP 250 总榜 (1~250 名自动翻页合并并导出 CSV)
    python tools/javdb-rankings/javdb_rankings_fetcher.py --top250 --export csv
"""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import sys
import time
from typing import Any
import urllib.request
import urllib.parse
import urllib.error

try:
    import requests
except ImportError:
    requests = None

JAVDB_BASE_URL = "https://jdforrepam.com"
JAVDB_HOST = "jdforrepam.com"
JAVDB_SIGNATURE_PREFIX = "lpw6vgqzsp"
JAVDB_SIGNATURE_SECRET = (
    "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
)

# 分类与周期映射字典
RANKING_TYPES = {
    "0": "全部",
    "1": "有码",
    "2": "无码",
    "3": "欧美",
}

PERIOD_NAMES = {
    "daily": "日榜",
    "weekly": "周榜",
    "monthly": "月榜",
}


def build_signature() -> str:
    """计算 JavDB 官方 App 请求所需的 jdSignature 动态签名."""
    timestamp = int(time.time())
    to_hash = f"{timestamp}{JAVDB_SIGNATURE_SECRET}".encode("utf-8")
    digest = hashlib.md5(to_hash).hexdigest()
    return f"{timestamp}.{JAVDB_SIGNATURE_PREFIX}.{digest}"


def javdb_request(path: str, params: dict[str, Any] | None = None, token: str | None = None) -> dict[str, Any]:
    """发送带有官方 App 签名与 Header 的 API 请求 (自动支持 requests 或内置 urllib)."""
    headers = {
        "User-Agent": "Dart/3.5 (dart:io)",
        "Accept-Language": "zh-TW",
        "Host": JAVDB_HOST,
        "jdSignature": build_signature(),
    }
    if token:
        headers["Authorization"] = f"Bearer {token.strip()}"

    # 1. 优先使用 requests (若已安装)
    if requests is not None:
        url = f"{JAVDB_BASE_URL}{path}"
        resp = requests.get(url, headers=headers, params=params, timeout=20)
        resp.raise_for_status()
        result = resp.json()
    else:
        # 2. 降级使用 Python 标准库 urllib (零依赖)
        query_str = urllib.parse.urlencode(params or {})
        url = f"{JAVDB_BASE_URL}{path}"
        if query_str:
            url = f"{url}?{query_str}"
        req = urllib.request.Request(url, headers=headers)
        with urllib.request.urlopen(req, timeout=20) as response:
            result = json.loads(response.read().decode("utf-8"))

    if not result.get("success"):
        raise RuntimeError(f"JavDB API 返回错误: {result.get('message')}")
    return result.get("data") or {}


def get_rankings(
    period: str = "weekly",
    ranking_type: str = "0",
    token: str | None = None,
) -> list[dict[str, Any]]:
    """获取排行榜数据 (日榜/周榜/月榜).

    参数:
        period: "daily" (日榜), "weekly" (周榜), "monthly" (月榜)
        ranking_type: "0" (全部), "1" (有码), "2" (无码), "3" (欧美)
        token: 可选用户 Token

    返回:
        电影列表 (按排名顺序)
    """
    valid_periods = {"daily", "weekly", "monthly"}
    if period not in valid_periods:
        raise ValueError(f"周期无效，仅支持: {valid_periods}")

    ranking_type = str(ranking_type).strip()
    if ranking_type not in RANKING_TYPES:
        ranking_type = "0"

    params = {
        "type": ranking_type,
        "period": period,
    }
    data = javdb_request("/api/v1/rankings", params=params, token=token)
    movies = data.get("movies", [])

    # 为每部电影打上实际排行名次字段
    for idx, movie in enumerate(movies, start=1):
        movie["rank"] = idx

    return movies


def get_top250_page(
    start_rank: int = 1,
    movie_type: str = "all",
    year: str | None = None,
    token: str | None = None,
) -> list[dict[str, Any]]:
    """获取 TOP 250 单页数据 (每页 50 部).

    参数:
        start_rank: 1, 51, 101, 151, 201
        movie_type: "all", "0", "1", "2", "3", "4"
        year: 可选四位年份，如 "2024"
    """
    if year:
        upstream_type = "year"
        upstream_type_value = str(year).strip()
    elif movie_type in {"0", "1", "2", "3", "4"}:
        upstream_type = "video_type"
        upstream_type_value = movie_type
    else:
        upstream_type = "all"
        upstream_type_value = ""

    params = {
        "start_rank": start_rank,
        "type": upstream_type,
        "type_value": upstream_type_value,
        "ignore_watched": "false",
        "page": 1,
        "limit": 50,
    }
    data = javdb_request("/api/v1/movies/top", params=params, token=token)
    movies = data.get("movies", [])

    for idx, movie in enumerate(movies, start=start_rank):
        movie["rank"] = idx

    return movies


def get_top250_full(
    movie_type: str = "all",
    year: str | None = None,
    token: str | None = None,
    delay_between_pages: float = 0.5,
) -> list[dict[str, Any]]:
    """获取完整的 TOP 250 (自动请求全部 5 页并合并为 1~250 名)."""
    full_list: list[dict[str, Any]] = []
    start_ranks = [1, 51, 101, 151, 201]

    for start_rank in start_ranks:
        print(f"[*] 正在抓取 TOP {start_rank} ~ {start_rank + 49}...")
        try:
            batch = get_top250_page(
                start_rank=start_rank,
                movie_type=movie_type,
                year=year,
                token=token,
            )
            full_list.extend(batch)
            if len(batch) < 50:
                # 后面已无更多
                break
        except Exception as exc:
            print(f"[-] 抓取分段 start_rank={start_rank} 失败: {exc}")
            break
        time.sleep(delay_between_pages)

    return full_list


def print_movie_list(title: str, movies: list[dict[str, Any]], max_items: int = 50) -> None:
    """在终端格式化输出榜单."""
    print(f"\n{'='*70}")
    print(f"  {title} (共 {len(movies)} 条)")
    print(f"{'='*70}")
    print(f"{'排名':<6}{'番号':<16}{'评分':<6}{'发行日期':<12}{'标题'}")
    print(f"{'-'*70}")

    for m in movies[:max_items]:
        rank = m.get("rank", "-")
        number = m.get("number", "未知番号")
        score = m.get("score") or "-"
        date = m.get("release_date") or m.get("publish_date") or "-"
        title_text = m.get("title", "")
        # 截断过长标题
        if len(title_text) > 30:
            title_text = title_text[:27] + "..."
        print(f"{rank:<6}{number:<16}{score:<6}{date:<12}{title_text}")

    if len(movies) > max_items:
        print(f"... 剩余 {len(movies) - max_items} 部未全部展示，请使用 --export 导出完整文件")
    print(f"{'='*70}\n")


def export_data(movies: list[dict[str, Any]], export_format: str, filename: str) -> None:
    """将数据导出为 JSON 或 CSV."""
    if export_format.lower() == "json":
        with open(filename, "w", encoding="utf-8") as f:
            json.dump(movies, f, ensure_ascii=False, indent=2)
        print(f"[+] 成功导出 JSON 数据到: {filename}")
    elif export_format.lower() == "csv":
        fieldnames = ["rank", "number", "title", "score", "release_date", "id", "cover_url"]
        with open(filename, "w", encoding="utf-8-sig", newline="") as f:
            writer = csv.DictWriter(f, fieldnames=fieldnames, extrasaction="ignore")
            writer.writeheader()
            for m in movies:
                writer.writerow(m)
        print(f"[+] 成功导出 CSV 数据到: {filename}")


def main() -> None:
    parser = argparse.ArgumentParser(description="JavDB 官方 App 排行榜与 TOP 250 独立抓取工具")
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument(
        "--ranking",
        choices=["daily", "weekly", "monthly"],
        help="抓取排行榜类型: daily (日榜), weekly (周榜), monthly (月榜)",
    )
    group.add_argument(
        "--top250",
        action="store_true",
        help="抓取 TOP 250 榜单 (默认拉取 1~250 名完整全量数据)",
    )

    parser.add_argument(
        "--type",
        default="0",
        help="影片分类过滤: 0=全部, 1=有码, 2=无码, 3=欧美 (默认 0)",
    )
    parser.add_argument(
        "--year",
        default=None,
        help="TOP 250 年份筛选 (如 2024, 2023)，仅在 --top250 时有效",
    )
    parser.add_argument(
        "--export",
        choices=["json", "csv"],
        default=None,
        help="导出文件格式: json 或 csv",
    )
    parser.add_argument(
        "--out",
        default=None,
        help="导出目标文件名 (如果不指定则自动生成)",
    )

    args = parser.parse_args()

    try:
        if args.ranking:
            period_str = PERIOD_NAMES.get(args.ranking, args.ranking)
            type_str = RANKING_TYPES.get(args.type, f"分类{args.type}")
            title = f"JavDB {period_str} ({type_str})"
            print(f"[*] 正在获取 {title}...")

            movies = get_rankings(period=args.ranking, ranking_type=args.type)
            print_movie_list(title, movies)

            if args.export:
                out_name = args.out or f"javdb_{args.ranking}_{args.type}.{args.export}"
                export_data(movies, args.export, out_name)

        elif args.top250:
            type_str = f"年份 {args.year}" if args.year else (RANKING_TYPES.get(args.type, "总榜"))
            title = f"JavDB TOP 250 ({type_str})"
            print(f"[*] 正在获取 {title}...")

            movies = get_top250_full(movie_type=args.type, year=args.year)
            print_movie_list(title, movies, max_items=50)

            if args.export:
                suffix = f"_{args.year}" if args.year else f"_{args.type}"
                out_name = args.out or f"javdb_top250{suffix}.{args.export}"
                export_data(movies, args.export, out_name)

    except Exception as exc:
        print(f"[-] 请求发生异常: {exc}", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
