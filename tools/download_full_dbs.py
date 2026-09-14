# -*- coding: utf-8 -*-
"""下载并解密 AVDB-Only 全量磁力数据库 (All_sehuatang & All_X1080X)."""

from __future__ import annotations

import os
import sys
import time
from pathlib import Path
import requests

base_dir = Path(__file__).resolve().parent.parent
tools_dir = Path(__file__).resolve().parent
sys.path.append(str(tools_dir))
from avdb_offline_decryptor import decrypt_avdb_zip

DOWNLOAD_DIR = base_dir / "data" / "downloads"
EXTRACT_DIR = base_dir / "data" / "raw_csv"

DOWNLOAD_DIR.mkdir(parents=True, exist_ok=True)
EXTRACT_DIR.mkdir(parents=True, exist_ok=True)

# 最新全量包下载地址
FULL_ASSETS = [
    {
        "name": "All_X1080X",
        "filename": "All_X1080X_117559_2026-09-13-14-02-27.zip",
        "url": "https://github.com/li-peifeng/AVdb-Only/releases/download/2026-09-13-14-02-27/All_X1080X_117559_2026-09-13-14-02-27.zip",
        "size_mb": 14.17,
    },
    {
        "name": "All_sehuatang",
        "filename": "All_sehuatang_326097_2026-09-13-14-02-27.zip",
        "url": "https://github.com/li-peifeng/AVdb-Only/releases/download/2026-09-13-14-02-27/All_sehuatang_326097_2026-09-13-14-02-27.zip",
        "size_mb": 45.11,
    },
]

# 备选镜像前缀 (若直连受阻可自动切换)
MIRRORS = [
    "",  # GitHub 原生
    "https://ghfast.top/",
    "https://gh-proxy.com/",
]


def download_with_fallback(asset: dict) -> Path:
    target_zip = DOWNLOAD_DIR / asset["filename"]
    if target_zip.exists() and target_zip.stat().st_size > 10 * 1024 * 1024:
        print(f"[+] 本地已存在完整压缩包: {target_zip.name} ({round(target_zip.stat().st_size/(1024*1024), 2)} MB)，跳过下载")
        return target_zip

    for mirror in MIRRORS:
        dl_url = mirror + asset["url"]
        print(f"[*] 尝试从 {mirror or 'GitHub原生'} 下载 {asset['name']} ({asset['size_mb']} MB)...")
        try:
            t0 = time.time()
            resp = requests.get(dl_url, stream=True, timeout=30)
            if resp.status_code != 200:
                print(f"[-] HTTP 状态码异常: {resp.status_code}")
                continue

            downloaded = 0
            with open(target_zip, "wb") as fp:
                for chunk in resp.iter_content(chunk_size=1024 * 512):
                    if chunk:
                        fp.write(chunk)
                        downloaded += len(chunk)
                        mb = downloaded / (1024 * 1024)
                        if int(mb) % 5 == 0 or downloaded == asset["size_mb"] * 1024 * 1024:
                            sys.stdout.write(f"\r    进度: {mb:.1f} MB / {asset['size_mb']} MB")
                            sys.stdout.flush()

            dt = time.time() - t0
            print(f"\n[+] 下载完成: {target_zip.name} (耗时 {dt:.1f}s, 速度 {downloaded/dt/(1024*1024):.2f} MB/s)")
            return target_zip
        except Exception as e:
            print(f"\n[-] 镜像 {mirror} 下载失败: {e}，尝试下一个...")

    raise RuntimeError(f"所有下载镜像均失败，无法获取 {asset['name']}")


def decrypt_and_extract(zip_path: Path, out_name: str) -> Path:
    out_csv = EXTRACT_DIR / out_name
    if out_csv.exists() and out_csv.stat().st_size > 10 * 1024 * 1024:
        print(f"[+] 本地已存在解密后的 CSV: {out_csv.name} ({round(out_csv.stat().st_size/(1024*1024), 2)} MB)，跳过解密")
        return out_csv

    print(f"[*] 正在读取并解密: {zip_path.name} ...")
    t0 = time.time()
    with open(zip_path, "rb") as fp:
        raw_zip_data = fp.read()

    fname, decrypted_bytes = decrypt_avdb_zip(raw_zip_data)
    with open(out_csv, "wb") as fp:
        fp.write(decrypted_bytes)

    dt = time.time() - t0
    print(f"[+] 解密完成！提取数据表: {out_csv.name} (原始名: {fname}, 大小: {round(len(decrypted_bytes)/(1024*1024), 2)} MB, 耗时 {dt:.2f}s)")
    return out_csv


def main():
    print("=======================================================")
    print("   AVDB 全量磁力数据库 (All_sehuatang & All_X1080X) 下载解密   ")
    print("=======================================================\n")

    results = {}
    for asset in FULL_ASSETS:
        zip_path = download_with_fallback(asset)
        csv_name = f"{asset['name']}_full.csv"
        csv_path = decrypt_and_extract(zip_path, csv_name)
        results[asset["name"]] = csv_path

    print("\n[+] 全量离线数据包已全部就绪:")
    for k, v in results.items():
        print(f"    - {k}: {v} ({round(v.stat().st_size/(1024*1024), 2)} MB)")


if __name__ == "__main__":
    main()
