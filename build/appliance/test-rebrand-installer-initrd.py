#!/usr/bin/env python3
# test-rebrand-installer-initrd.py — lib/rebrand-installer-initrd.py 的離線
# 回歸測試。完全不需要 cpio 指令、不需要真的 Debian initrd、不需要開機:
# 用手工組出來的合成 newc cpio(含一張假的 logo_installer.png)當輸入,
# 驗證「解壓→拆 cpio→換 logo→打包→壓回去」這條邏輯本身是對的。
#
# 這裡驗證的重點:
#   1. 無修改時 parse→emit 要 byte-for-byte 一模一樣(獨立手寫一份 newc
#      writer 當對照,確認我的 emit 沒有寫壞 padding/表頭)。
#   2. 換 logo 後:該檔內容真的變成新 PNG、其他檔案 byte 不變、trailer
#      還在、整包還能重新解析。
#   3. gzip 與 xz 兩種容器都能來回。
#   4. 多個 gzip 串流串接(early cpio + main)的情況能正確解出。
#   5. 找不到 logo 時回傳碼 2、檔案不動。
#
# 用法:  python3 build/appliance/test-rebrand-installer-initrd.py
# 成功印出各項 PASS、exit 0;任何一項失敗印 FAIL 並 exit 1。

import os, sys, gzip, lzma, importlib.util, tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location(
    "rb", os.path.join(HERE, "lib", "rebrand-installer-initrd.py")
)
rb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rb)

FAIL = 0


def check(name, cond):
    global FAIL
    print(("PASS: " if cond else "FAIL: ") + name)
    if not cond:
        FAIL = 1


# --- 獨立手寫的 newc cpio writer(當對照組,不呼叫被測模組) -----------
def newc_entry(name, data, mode=0o100644, ino=1):
    name_b = name.encode() + b"\x00"
    hdr = b"070701"
    hdr += b"%08x" % ino
    hdr += b"%08x" % mode
    hdr += b"%08x" % 0      # uid
    hdr += b"%08x" % 0      # gid
    hdr += b"%08x" % 1      # nlink
    hdr += b"%08x" % 0      # mtime
    hdr += b"%08x" % len(data)
    hdr += b"%08x" % 0 * 0
    hdr += b"%08x" % 3      # devmajor
    hdr += b"%08x" % 1      # devminor
    hdr += b"%08x" % 0      # rdevmajor
    hdr += b"%08x" % 0      # rdevminor
    hdr += b"%08x" % len(name_b)
    hdr += b"%08x" % 0      # check
    assert len(hdr) == 110, len(hdr)
    out = bytearray(hdr)
    out += name_b
    out += b"\x00" * ((-(110 + len(name_b))) % 4)
    out += data
    out += b"\x00" * ((-len(data)) % 4)
    return bytes(out)


def build_cpio(files):
    out = bytearray()
    ino = 1
    for name, data in files:
        out += newc_entry(name, data, ino=ino)
        ino += 1
    out += newc_entry("TRAILER!!!", b"", mode=0, ino=0)
    # cpio 慣例把整包補到 512 的倍數(模擬真實 initrd 的尾端填塞)
    out += b"\x00" * ((-len(out)) % 512)
    return bytes(out)


# 一張「原始」假 PNG(有合法 IHDR,能被 _png_dims 讀到尺寸)跟一張
# 「GoNAS」假 PNG(不同尺寸、不同內容)。用 PIL 產生真 PNG;PIL 在這個
# 沙盒有(被測模組的 resize 分支也才測得到)。
from PIL import Image
import io

def png(w, h, color):
    buf = io.BytesIO()
    Image.new("RGBA", (w, h), color).save(buf, format="PNG")
    return buf.getvalue()

orig_logo = png(300, 100, (100, 100, 200, 255))
gonas_logo = png(360, 96, (47, 139, 128, 255))

FILES = [
    ("./init", b"#!/bin/sh\necho hi\n"),
    ("usr/share/graphics/logo_installer.png", orig_logo),
    ("usr/share/graphics/other.png", png(10, 10, (0, 0, 0, 255))),
    ("etc/hostname", b"installer\n"),
]

cpio = build_cpio(FILES)

# --- 1. 無修改 parse→emit 要一模一樣 ----------------------------------
entries, tail = rb._parse_newc(cpio)
roundtrip = rb._emit_newc(entries, tail)
check("parse->emit is byte-identical (no changes)", roundtrip == cpio)

# --- 2. gzip 容器:換 logo -------------------------------------------
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd.gz")
    logo = os.path.join(d, "gonas.png")
    with open(logo, "wb") as f:
        f.write(gonas_logo)
    with open(initrd, "wb") as f:
        f.write(gzip.compress(cpio, 9))
    rc, replaced = rb.rebrand(initrd, logo)
    check("gzip: return code 0 (replaced)", rc == 0)
    check("gzip: exactly one logo replaced", len(replaced) == 1)
    # 解回來驗證
    with open(initrd, "rb") as f:
        new_raw = f.read()
    new_cpio = rb._gunzip_all(new_raw)
    ents, _ = rb._parse_newc(new_cpio)
    byname = {e[1].decode(): e[3] for e in ents}
    new_logo = byname["usr/share/graphics/logo_installer.png"]
    check("gzip: logo bytes changed from the original Debian logo",
          new_logo != orig_logo)
    # 有 PIL 時模組會把 GoNAS logo 縮回原圖尺寸(版面才不跑掉),所以存進去
    # 的是「縮放後」的 PNG,不是原始檔案 bytes——這裡驗尺寸被縮回 300x100。
    check("gzip: GoNAS logo resized back to original 300x100",
          rb._png_dims(new_logo) == (300, 100))
    check("gzip: non-logo file untouched",
          byname["etc/hostname"] == b"installer\n")
    check("gzip: other.png untouched",
          byname["usr/share/graphics/other.png"] == png(10, 10, (0, 0, 0, 255)))
    check("gzip: TRAILER!!! still present", "TRAILER!!!" in byname)
    check("gzip: output still valid gzip", new_raw[:2] == b"\x1f\x8b")

# --- 3. xz 容器 -------------------------------------------------------
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd.xz")
    logo = os.path.join(d, "gonas.png")
    with open(logo, "wb") as f:
        f.write(gonas_logo)
    with open(initrd, "wb") as f:
        f.write(lzma.compress(cpio, preset=6))
    rc, replaced = rb.rebrand(initrd, logo)
    with open(initrd, "rb") as f:
        new_raw = f.read()
    check("xz: return code 0", rc == 0)
    check("xz: output still valid xz", new_raw[:6] == b"\xfd7zXZ\x00")
    ents, _ = rb._parse_newc(lzma.decompress(new_raw))
    byname = {e[1].decode(): e[3] for e in ents}
    xz_logo = byname["usr/share/graphics/logo_installer.png"]
    check("xz: logo replaced (changed, resized to 300x100)",
          xz_logo != orig_logo and rb._png_dims(xz_logo) == (300, 100))

# --- 4. 多個 gzip 串流串接 -------------------------------------------
concat = gzip.compress(b"", 9) + gzip.compress(cpio, 9)
check("concatenated gzip members decode to the cpio", rb._gunzip_all(concat) == cpio)

# --- 5. 找不到 logo → 回傳 2、檔案不動 --------------------------------
nolog = build_cpio([("etc/hostname", b"x\n"), ("bin/busybox", b"ELF...")])
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd.gz")
    logo = os.path.join(d, "gonas.png")
    with open(logo, "wb") as f:
        f.write(gonas_logo)
    before = gzip.compress(nolog, 9)
    with open(initrd, "wb") as f:
        f.write(before)
    rc, replaced = rb.rebrand(initrd, logo)
    with open(initrd, "rb") as f:
        after = f.read()
    check("no-logo: return code 2", rc == 2)
    check("no-logo: file left byte-identical", after == before)

print()
if FAIL:
    print("==> one or more rebrand-initrd test cases FAILED")
    sys.exit(1)
print("==> all rebrand-initrd test cases passed")
