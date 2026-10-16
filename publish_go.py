#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Go 版一键发布脚本：把 go-timesync 打包成单文件 exe + 分享 zip + 自解压 exe。

用法：
  python publish_go.py            # 构建 + 分享 zip + 自解压 exe
  python publish_go.py --clean    # 先清旧构建再打包

依赖：
  - Go 1.22（已装在 C:/Users/suzhe/.workbuddy/binaries/go/go/bin/go.exe）
  - 7-Zip（C:/Program Files/7-Zip/7z.exe + 7z.sfx 做自解压外壳）

自解压 exe 依赖本机 7-Zip 的 7z.sfx 模块；若不可用则跳过并提示，不影响 zip 产出。
"""
import os
import shutil
import subprocess
import sys
import zipfile

HERE = os.path.dirname(os.path.abspath(__file__))
DIST = os.path.join(HERE, "dist")
ZIP = os.path.join(HERE, "自动校时工具_Go_分享包.zip")
SFX = os.path.join(HERE, "自动校时工具_Go_自解压.exe")
SEVEN = r"C:\Program Files\7-Zip\7z.exe"
SFX_MOD = r"C:\Program Files\7-Zip\7z.sfx"
GO = r"C:\Users\suzhe\.workbuddy\binaries\go\go\bin\go.exe"
WINRES = r"C:\Users\suzhe\go\bin\go-winres.exe"
ICON = os.path.join(HERE, "icon.ico")

# (源路径, 压缩包内相对路径)；顶层统一放 自动校时工具\ 下
INCLUDES = [
    (os.path.join(DIST, "timesync.exe"), "自动校时工具/timesync.exe"),
    ("配置.ini", "自动校时工具/配置.ini"),
    ("使用说明.html", "自动校时工具/使用说明.html"),
    ("README.md", "自动校时工具/说明.md"),
    ("LICENSE", "自动校时工具/许可证"),
]

# 源码包里的非 .go 附属文件（前端页 + 图标生成器）
SRC_WEB = [
    ("web/index.html", "自动校时工具/src/web/index.html"),
    ("tools/makeicon/main.go", "自动校时工具/src/tools/makeicon/main.go"),
    ("tools/makeicon/ico.go", "自动校时工具/src/tools/makeicon/ico.go"),
]

def collect_src_files():
    """自动收集根目录下的 Go 源码与模块文件。

    早先是硬编码列表，新增 .go 文件（如 diag.go）会被静默漏掉，导致分发包里
    的源码与 exe 不同步。改为自动扫描，保证包内源码始终可复现当前二进制。
    """
    import glob as _glob
    files = sorted(_glob.glob(os.path.join(HERE, "*.go")))
    # 图标（重建 exe 时需要，配合 tools/makeicon 可重新生成）
    files.append(os.path.join(HERE, "icon.ico"))
    for extra in ("go.mod", "go.sum"):
        p = os.path.join(HERE, extra)
        if os.path.exists(p):
            files.append(p)
    return [os.path.basename(f) for f in files if os.path.exists(f)]

SRC_FILES = collect_src_files()


def safe_remove(path):
    try:
        if os.path.isdir(path):
            shutil.rmtree(path)
        elif os.path.exists(path):
            os.remove(path)
    except Exception as e:  # noqa: BLE001
        print("  (跳过清理 %s：%s)" % (os.path.basename(path), e))


def read_version():
    """从 defaults.go 的 `const Version = "x.y.z"` 读取版本，避免与源码脱节。"""
    try:
        with open(os.path.join(HERE, "defaults.go"), encoding="utf-8") as f:
            for line in f:
                if "Version" in line and "=" in line and '"' in line:
                    return line.split('"')[1]
    except OSError:
        pass
    return "0.0.0"


def ensure_resources():
    """生成 Windows 资源对象 rsrc_windows_amd64.syso（图标 + 版本信息）。

    go build 会自动链接包目录里的 *.syso，从而让 exe 在资源管理器显示图标、
    右键属性显示产品名/版本号。缺 go-winres 或 icon.ico 时降级跳过（不影响构建）。
    """
    if not os.path.exists(WINRES):
        print("  (未找到 go-winres，跳过 图标/版本信息)")
        return
    if not os.path.exists(ICON):
        print("  (未找到 icon.ico，跳过 图标/版本信息)")
        return
    ver = read_version()
    cmd = [WINRES, "simply", "--arch", "amd64",
           "--icon", ICON, "--manifest", "cli",
           "--product-name", "time_sync 自动校时工具",
           "--file-description", "自动校时工具（NTP + HTTP 兜底）",
           "--product-version", ver, "--file-version", ver,
           "--copyright", "MIT License",
           "--original-filename", "timesync.exe",
           "--out", "rsrc"]
    print(">> 生成资源（图标 + 版本 %s）" % ver)
    subprocess.run(cmd, check=True, cwd=HERE)


def build(clean):
    if clean:
        safe_remove(DIST)
    os.makedirs(DIST, exist_ok=True)
    ensure_resources()
    out = os.path.join(DIST, "timesync.exe")
    env = dict(os.environ)
    # 国内拉取 x/sys 用 goproxy.cn；锁定到 Go 1.22，避免自动下载 1.26 toolchain（需外网）
    env.setdefault("GOPROXY", "https://goproxy.cn,direct")
    env["GOTOOLCHAIN"] = "local"
    cmd = [GO, "build", "-ldflags", "-s -w", "-o", out, "."]
    print(">> " + " ".join(cmd))
    subprocess.run(cmd, check=True, cwd=HERE, env=env)
    print("  已产出 %s（%.2f MB）" % (out, os.path.getsize(out) / 1024 / 1024))


def make_zip():
    if os.path.exists(ZIP):
        os.remove(ZIP)
    with zipfile.ZipFile(ZIP, "w", zipfile.ZIP_DEFLATED) as z:
        for f, arc in INCLUDES:
            src = os.path.join(HERE, f)
            if not os.path.exists(src):
                print("  (缺失，跳过 %s)" % f)
                continue
            z.write(src, arc)
        # 源码
        for name in SRC_FILES:
            src = os.path.join(HERE, name)
            if os.path.exists(src):
                z.write(src, "自动校时工具/src/" + name)
        for rel, arc in SRC_WEB:
            src = os.path.join(HERE, rel)
            if os.path.exists(src):
                z.write(src, arc)
    with zipfile.ZipFile(ZIP) as z:
        ok = "OK" if z.testzip() is None else "损坏"
        print("已生成 %s（校验 %s，%d 个条目）" % (ZIP, ok, len(z.infolist())))
        for i in z.infolist():
            print("   ", i.filename, round(i.file_size / 1024, 1), "KB")


def make_sfx():
    """可选：把分享 zip 再封装成自解压 exe（双击自动解压到 自动校时工具\）。
    依赖本机 7-Zip 的 7z.sfx 模块；若 7-Zip 不可用则跳过并提示，不影响 zip 产出。"""
    if not (os.path.exists(SEVEN) and os.path.exists(SFX_MOD)):
        print("· 跳过自解压 exe（未找到 7-Zip 的 7z.exe / 7z.sfx；可安装 7-Zip 后重跑）")
        return
    if not os.path.exists(ZIP):
        print("· 跳过自解压 exe（分享包 %s 不存在）" % os.path.basename(ZIP))
        return
    stage = os.path.join(HERE, "sfx_stage")
    archive = os.path.join(HERE, "sfx_payload.7z")
    config = os.path.join(HERE, "sfx_config.txt")
    try:
        if os.path.exists(stage):
            shutil.rmtree(stage)
        os.makedirs(stage, exist_ok=True)
        # 展开分享 zip，去掉 自动校时工具\ 前缀（压平），让解压后直接落在 自动校时工具\ 内
        with zipfile.ZipFile(ZIP) as z:
            for name in z.namelist():
                if name.endswith("/"):
                    continue
                rel = name.split("/", 1)[1] if name.startswith("自动校时工具/") else name
                dst = os.path.join(stage, rel)
                os.makedirs(os.path.dirname(dst), exist_ok=True)
                with open(dst, "wb") as f:
                    f.write(z.read(name))
        if os.path.exists(archive):
            os.remove(archive)
        subprocess.run([SEVEN, "a", "-t7z", archive, "."], cwd=stage, check=True)
        # 7z.sfx 配置：UTF-8 需带 BOM，否则中文 Path 乱码
        cfg = (';!@Install@!UTF-8!\n'
               'Title="自动校时工具 (Go 版)"\n'
               'Path="自动校时工具"\n'
               'Silent=1\n'
               'Overwrite=1\n'
               ';!@InstallEnd@!\n')
        with open(config, "w", encoding="utf-8-sig") as f:
            f.write(cfg)
        with open(SFX, "wb") as out:
            for part in (SFX_MOD, config, archive):
                with open(part, "rb") as f:
                    shutil.copyfileobj(f, out)
        ok = open(SFX, "rb").read(2) == b"MZ"
        print("已生成 %s（%s，%.1f KB）" % (
            SFX, "MZ 有效" if ok else "头无效!", os.path.getsize(SFX) / 1024))
    finally:
        for p in (stage, archive, config):
            if os.path.isdir(p):
                shutil.rmtree(p)
            elif os.path.exists(p):
                os.remove(p)


if __name__ == "__main__":
    build("--clean" in sys.argv)
    make_zip()
    make_sfx()
    print("发布完成 ->", ZIP)
    print("自解压版   ->", SFX)
