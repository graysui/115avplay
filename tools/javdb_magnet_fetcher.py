# -*- coding: utf-8 -*-
"""JavDB 在线磁力与评论提取工具 (独立运行版).

依赖:
    pip install requests

使用方式:
    python tools/javdb_magnet_fetcher.py [番号]
    例如:
    python tools/javdb_magnet_fetcher.py SSIS-123
"""

from __future__ import annotations

import hashlib
import html
import re
import sys
import time
from typing import Any
from urllib.parse import quote
import requests

JAVDB_BASE_URL = "https://jdforrepam.com"
JAVDB_HOST = "jdforrepam.com"
JAVDB_SIGNATURE_PREFIX = "lpw6vgqzsp"
JAVDB_SIGNATURE_SECRET = (
    "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
)

# 评论区提取正则
_MAGNET_PATTERN = re.compile(r"(?i)magnet:\?[^\s<>\"'`\[\]{}]+")
_ED2K_PATTERN = re.compile(r"(?is)ed2k://\|(?:file|folder)\|.{0,4096}?\|/")
_TRAILING_PUNCTUATION = ".,;:!?)]}'>\"，。；：！？）】》"


def build_signature() -> str:
    """计算 JavDB 官方 App 请求所需的 jdSignature 动态签名."""
    timestamp = int(time.time())
    to_hash = f"{timestamp}{JAVDB_SIGNATURE_SECRET}".encode("utf-8")
    digest = hashlib.md5(to_hash).hexdigest()
    return f"{timestamp}.{JAVDB_SIGNATURE_PREFIX}.{digest}"


def javdb_request(path: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
    """发送带有官方 App 签名与 Header 的 API 请求."""
    url = f"{JAVDB_BASE_URL}{path}"
    headers = {
        "User-Agent": "Dart/3.5 (dart:io)",
        "Accept-Language": "zh-TW",
        "Host": JAVDB_HOST,
        "jdSignature": build_signature(),
    }
    resp = requests.get(url, headers=headers, params=params, timeout=15)
    resp.raise_for_status()
    result = resp.json()
    if not result.get("success"):
        raise RuntimeError(f"JavDB API 报错: {result.get('message')}")
    return result.get("data") or {}


def search_movie(number: str) -> dict[str, Any] | None:
    """按番号搜索影片，返回首个匹配影片的基本信息（含 movie_id）."""
    data = javdb_request(
        "/api/v2/search",
        {
            "q": number.strip(),
            "type": "movie",
            "movie_type": "all",
            "page": 1,
            "limit": 5,
        },
    )
    movies = data.get("movies", [])
    if not movies:
        return None
    # 优先精确比对番号
    target_clean = re.sub(r"[^a-zA-Z0-9]", "", number).lower()
    for m in movies:
        m_num = re.sub(r"[^a-zA-Z0-9]", "", str(m.get("number", ""))).lower()
        if m_num == target_clean:
            return m
    return movies[0]


def get_official_magnets(movie_id: str) -> list[dict[str, Any]]:
    """获取影片官方页面收录的磁力链接."""
    data = javdb_request(f"/api/v1/movies/{quote(movie_id, safe='')}/magnets")
    return data.get("magnets", [])


def extract_links_from_text(text: str) -> list[str]:
    """从评论纯文本中提取并清洗出磁力/ED2K 链接."""
    cleaned = text or ""
    for _ in range(3):
        cleaned = html.unescape(cleaned)

    found = []
    found.extend(m.group(0) for m in _MAGNET_PATTERN.finditer(cleaned))
    found.extend(m.group(0) for m in _ED2K_PATTERN.finditer(cleaned))

    valid = []
    for raw in found:
        link = raw.rstrip(_TRAILING_PUNCTUATION).strip()
        if link.lower().startswith("magnet:?") and "xt=urn:btih:" in link.lower():
            valid.append(link)
        elif link.lower().startswith("ed2k://"):
            valid.append(link)
    return list(dict.fromkeys(valid))


def get_comment_magnets(movie_id: str) -> list[dict[str, Any]]:
    """获取用户评论列表并提取其中分享的隐藏磁力."""
    data = javdb_request(
        f"/api/v1/movies/{quote(movie_id, safe='')}/reviews",
        {
            "page": 1,
            "limit": 20,
        },
    )
    reviews = data.get("reviews", [])
    extracted_resources = []
    for r in reviews:
        content = r.get("content", "")
        links = extract_links_from_text(content)
        if links:
            extracted_resources.append(
                {
                    "user_name": (r.get("user") or {}).get("name", "匿名用户"),
                    "score": r.get("score"),
                    "date": r.get("created_at"),
                    "links": links,
                }
            )
    return extracted_resources


def main() -> None:
    test_number = sys.argv[1].strip() if len(sys.argv) > 1 else "SSIS-123"
    print(f"[*] 正在检索番号: {test_number}...")

    try:
        movie = search_movie(test_number)
    except Exception as exc:
        print(f"[-] 请求 JavDB 失败: {exc}")
        return

    if not movie:
        print(f"[-] 未找到番号 {test_number} 相关影片")
        return

    movie_id = movie.get("id")
    title = movie.get("title")
    print(f"[+] 匹配成功: ID={movie_id} | 番号={movie.get('number')} | 标题={title}\n")

    # 1. 提取官方磁链
    print("=== [官方收录磁力列表] ===")
    try:
        magnets = get_official_magnets(movie_id)
        if not magnets:
            print("暂无官方磁力")
        for i, m in enumerate(magnets, 1):
            size_mb = (
                round(m.get("size_bytes", 0) / (1024 * 1024), 1)
                if m.get("size_bytes")
                else m.get("size_mb", 0)
            )
            hd_tag = "[HD]" if m.get("has_hd") else ""
            sub_tag = "[中字]" if m.get("has_sub") else ""
            print(
                f"[{i}] {hd_tag}{sub_tag} {m.get('name', '未知文件名')} "
                f"({size_mb} MB) 做种数: {m.get('seeders', 0)}"
            )
            print(f"    磁链: {m.get('magnet_url')}\n")
    except Exception as exc:
        print(f"[-] 读取官方磁力失败: {exc}")

    # 2. 提取评论区磁链
    print("=== [评论区隐藏分享资源] ===")
    try:
        comment_resources = get_comment_magnets(movie_id)
        if not comment_resources:
            print("评论区中未发现分享的磁链/电驴链接")
        for cr in comment_resources:
            print(f"用户 [{cr['user_name']}] 在 {cr['date']} 分享:")
            for link in cr["links"]:
                print(f"    --> {link}")
    except Exception as exc:
        print(f"[-] 读取评论区资源失败: {exc}")


if __name__ == "__main__":
    main()
