# -*- coding: utf-8 -*-
"""AVDB 离线磁力资源库解密与下载工具 (独立运行版).

依赖:
    pip install cryptography requests

使用方式:
    1. 自动从 GitHub/Gitee 下载最新增量包并解密:
       python tools/avdb_offline_decryptor.py

    2. 解密本地已下载好的加密 ZIP 包:
       python tools/avdb_offline_decryptor.py <path_to_encrypted_zip>
"""

from __future__ import annotations

import base64
import hashlib
import io
import json
import os
import sys
import zipfile
from pathlib import Path
from typing import Any
import requests

try:
    from cryptography.hazmat.primitives.ciphers.aead import AESGCM
except ImportError:
    raise ImportError("缺少加密库依赖，请先安装: pip install cryptography requests")

# AVDB 固定主密钥摘要 (CA42E687...)
_RESOURCE_LIBRARY_PASSWORD_DIGEST = bytes.fromhex(
    "ca42e687df5818e2e88da0ff5b9fd2c60f7e22721f682b66c3e50485a00d06d5"
)
MANIFEST_FILENAME = "avdb-resource-library.json"


def decrypt_avdb_zip(encrypted_zip_bytes: bytes) -> tuple[str, bytes]:
    """解密 AVDB 离线加密 ZIP 包.

    返回: (内部数据文件名, 解密后的数据文件内容二进制)
    """
    with zipfile.ZipFile(io.BytesIO(encrypted_zip_bytes), mode="r") as outer_zip:
        file_list = set(outer_zip.namelist())

        # 1. 检查是否为加密包
        if MANIFEST_FILENAME not in file_list:
            candidates = [
                f for f in file_list if f.lower().endswith((".csv", ".xlsx", ".xls"))
            ]
            if candidates:
                preferred = sorted(candidates, key=lambda x: (x.count("/"), len(x)))[0]
                return preferred, outer_zip.read(preferred)
            raise ValueError("ZIP 中未包含加密清单或可识别的数据表")

        # 2. 读取加密清单
        manifest_raw = outer_zip.read(MANIFEST_FILENAME).decode("utf-8")
        manifest = json.loads(manifest_raw)

        payload_name = manifest.get("payload", "avdb-resource-library.bin")
        if payload_name not in file_list:
            raise ValueError(f"缺少密文载荷文件: {payload_name}")

        salt = base64.b64decode(manifest["salt"])
        nonce = base64.b64decode(manifest["nonce"])
        tag = base64.b64decode(manifest["tag"])
        iterations = int(manifest.get("iterations", 200_000))
        ciphertext = outer_zip.read(payload_name)

    # 3. PBKDF2 密钥派生 (SHA256, 200,000 次迭代)
    aes_key = hashlib.pbkdf2_hmac(
        "sha256",
        _RESOURCE_LIBRARY_PASSWORD_DIGEST,
        salt,
        iterations,
        dklen=32,
    )

    # 4. AES-256-GCM 解密
    aesgcm = AESGCM(aes_key)
    try:
        decrypted_inner_zip = aesgcm.decrypt(nonce, ciphertext + tag, None)
    except Exception as exc:
        raise ValueError("解密失败：Tag 认证不通过或数据包损坏") from exc

    # 5. 从内层 ZIP 中提取出 CSV / Excel 表格
    with zipfile.ZipFile(io.BytesIO(decrypted_inner_zip), mode="r") as inner_zip:
        candidates = [
            f for f in inner_zip.namelist() if f.lower().endswith((".csv", ".xlsx", ".xls"))
        ]
        if not candidates:
            raise ValueError("解密后的内层 ZIP 中未找到 CSV/Excel 数据文件")
        preferred = sorted(candidates, key=lambda x: (x.count("/"), len(x)))[0]
        return preferred, inner_zip.read(preferred)


def fetch_latest_release_asset(
    platform: str = "github",
    site_prefix: str = "30D_sehuatang",  # 或 "All_sehuatang", "All_X1080X"
) -> tuple[str, str]:
    """获取最新 Release 的下载地址 (支持 github 或 gitee)."""
    if platform.lower() == "gitee":
        api_url = "https://gitee.com/api/v5/repos/avdb/avdb-only/releases/latest"
    else:
        api_url = "https://api.github.com/repos/li-peifeng/AVdb-Only/releases/latest"

    print(f"[*] 正在从 {platform} 获取最新 Release 清单...")
    resp = requests.get(api_url, timeout=15)
    resp.raise_for_status()
    release_info = resp.json()

    assets = release_info.get("assets", [])
    for asset in assets:
        name = asset.get("name", "")
        if name.startswith(site_prefix) and name.endswith(".zip"):
            download_url = asset.get("browser_download_url")
            return name, download_url

    raise FileNotFoundError(f"在最新 Release 中未匹配到前缀为 {site_prefix} 的资源包")


def main() -> None:
    if len(sys.argv) > 1 and os.path.isfile(sys.argv[1]):
        local_path = sys.argv[1]
        print(f"[*] 正在解密本地文件: {local_path}")
        with open(local_path, "rb") as fp:
            data = fp.read()
        filename, content = decrypt_avdb_zip(data)
        out_name = f"decrypted_{Path(filename).name}"
        with open(out_name, "wb") as fp:
            fp.write(content)
        print(f"[+] 解密成功！已保存提取的数据至: {out_name} (大小: {len(content)} 字节)")
    else:
        try:
            asset_name, dl_url = fetch_latest_release_asset(
                platform="github", site_prefix="30D_sehuatang"
            )
            print(f"[+] 找到最新发布包: {asset_name}")
            print(f"[+] 下载链接: {dl_url}")
            print("[*] 开始下载...")
            r = requests.get(dl_url, timeout=60)
            r.raise_for_status()

            filename, csv_data = decrypt_avdb_zip(r.content)
            out_file = f"decrypted_{Path(filename).name}"
            with open(out_file, "wb") as f:
                f.write(csv_data)
            print(f"[+] 下载并解密完成！数据已保存至: {out_file} (大小: {len(csv_data)} 字节)")
        except Exception as e:
            print(f"[-] 执行失败: {e}")


if __name__ == "__main__":
    main()
