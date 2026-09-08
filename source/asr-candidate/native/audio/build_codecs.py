#!/usr/bin/env python3
"""把固定官方 Opus 与保留源码的 SpanDSP G.722 构建到显式私有目录，不安装系统库。"""
import argparse
import hashlib
import json
import os
import pathlib
import platform
import re
import subprocess
import tarfile
import urllib.request

# 固定归档与校验和使网络下载可复现；不跟随浮动 latest 分支。
OPUS_URL = "https://downloads.xiph.org/releases/opus/opus-1.5.2.tar.gz"
OPUS_SHA256 = "65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1"
MAX_ARCHIVE_BYTES = 16 * 1024 * 1024


def run(command, cwd=None):
    """保留构建输出，子进程失败立即停止，禁止把缺少的库报告为成功。"""
    env = os.environ.copy()
    # 受限 macOS 禁止 sysctl 时，给 libtool 保守命令长度，避免空探测结果丢失链接对象。
    env.setdefault("lt_cv_sys_max_cmd_len", "16384")
    subprocess.run(command, cwd=cwd, check=True, env=env)


def read_archive(path):
    """本地和网络归档使用相同大小上限及摘要校验，避免读取无限响应。"""
    stream = open(path, "rb") if path else urllib.request.urlopen(OPUS_URL, timeout=60)
    with stream:
        data = stream.read(MAX_ARCHIVE_BYTES + 1)
    if len(data) > MAX_ARCHIVE_BYTES or hashlib.sha256(data).hexdigest() != OPUS_SHA256:
        raise SystemExit("Opus归档长度或SHA256校验失败")
    return data


def verify_vendor(here):
    """核对保留的每个上游文件；需要修改上游源码时必须同步来源记录。"""
    vendor = here.parent / "vendor/spandsp"
    manifest = json.loads((here / "sources.json").read_text())
    for item in manifest["spandsp"]["files"]:
        path = vendor / item["path"]
        if hashlib.sha256(path.read_bytes()).hexdigest() != item["sha256"]:
            raise SystemExit("上游源码校验失败: " + item["path"])
    return vendor / "src"


def main():
    """校验归档路径与源身份，再执行本地受信编译器和官方构建脚本。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prefix", required=True, help="仅写入此私有输出目录")
    parser.add_argument("--opus-archive", help="可选已下载官方归档，仍校验SHA256")
    args = parser.parse_args()
    if platform.system() not in ("Darwin", "Linux"):
        raise SystemExit("此构建脚本当前只支持macOS和Linux")
    root = pathlib.Path(args.prefix).resolve()
    root.mkdir(parents=True, exist_ok=True)
    here = pathlib.Path(__file__).resolve().parent
    vendor = verify_vendor(here)
    archive = root / "opus-1.5.2.tar.gz"
    archive.write_bytes(read_archive(args.opus_archive))
    source = root / "opus-1.5.2"
    if not source.exists():
        with tarfile.open(archive) as pack:
            for entry in pack.getmembers():
                path = (root / entry.name).resolve()
                if (root not in path.parents or not (entry.isfile() or entry.isdir())
                        or pathlib.PurePosixPath(entry.name).parts[0] != "opus-1.5.2"):
                    raise SystemExit("归档路径或文件类型非法")
            pack.extractall(root)
    run(["./configure", "--enable-shared", "--disable-static", "--disable-doc",
         "--disable-extra-programs", "--disable-intrinsics", "--prefix=" + str(root / "opus")], source)
    run(["make", "-j4"], source)
    run(["make", "install"], source)
    # 支持的Unix平台都有标准浮点函数；宏防止上游为古老平台生成同名替代函数。
    macros = set(re.findall(r"HAVE_[A-Z0-9_]+", (vendor / "floating_fudge.h").read_text()
                           + (vendor / "spandsp/fast_convert.h").read_text()))
    flags = ["-D" + macro + "=1" for macro in sorted(macros | {"HAVE_MATH_H", "HAVE_STDBOOL_H"})]
    ext = "dylib" if platform.system() == "Darwin" else "so"
    library = root / ("librustswitch_g722." + ext)
    run([os.environ.get("CC", "cc"), "-O2", "-fPIC", "-dynamiclib" if ext == "dylib" else "-shared",
         *flags, "-I" + str(vendor), str(vendor / "g722.c"), str(here / "g722_support.c"),
         "-lm", "-o", str(library)])
    print(json.dumps({"g722_library": str(library),
                      "opus_library": str(root / "opus/lib" / ("libopus." + ext)),
                      "opus_source_sha256": OPUS_SHA256}, ensure_ascii=False))


if __name__ == "__main__":
    main()
