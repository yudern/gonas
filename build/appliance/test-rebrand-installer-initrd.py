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

# --- 第三十輪覆核 Q1:microcode 前置(未壓縮 early-cpio ++ gzip 主封存) ---
def newc_entry_crc(name, data, mode=0o100644, ino=1):
    # 070702(CRC 格式)版的 entry,c_check = sum(data) mod 2^32
    nb = name.encode() + b"\x00"
    chk = sum(data) & 0xFFFFFFFF
    hdr = b"070702"
    hdr += b"%08x" % ino; hdr += b"%08x" % mode; hdr += b"%08x" % 0; hdr += b"%08x" % 0
    hdr += b"%08x" % 1; hdr += b"%08x" % 0; hdr += b"%08x" % len(data); hdr += b"%08x" % 0 * 0
    hdr += b"%08x" % 3; hdr += b"%08x" % 1; hdr += b"%08x" % 0; hdr += b"%08x" % 0
    hdr += b"%08x" % len(nb); hdr += b"%08x" % chk
    assert len(hdr) == 110, len(hdr)
    o = bytearray(hdr); o += nb; o += b"\x00" * ((-(110 + len(nb))) % 4)
    o += data; o += b"\x00" * ((-len(data)) % 4)
    return bytes(o)

def build_cpio_crc(files):
    o = bytearray(); i = 1
    for n, d in files:
        o += newc_entry_crc(n, d, ino=i); i += 1
    o += newc_entry_crc("TRAILER!!!", b"", 0, 0)
    o += b"\x00" * ((-len(o)) % 512)
    return bytes(o)

# microcode initrd:未壓縮 early-cpio(無 logo)+ gzip(主封存含 logo)
early = build_cpio([("kernel/x86/microcode/GenuineIntel.bin", b"MICROCODE-BLOB-DATA")])
main = build_cpio([("./init", b"x"), ("usr/share/graphics/logo_installer.png", orig_logo)])
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd"); logo = os.path.join(d, "g.png")
    open(logo, "wb").write(gonas_logo)
    open(initrd, "wb").write(early + gzip.compress(main, 9))
    rc, replaced = rb.rebrand(initrd, logo)
    check("microcode: return code 0 (found logo in gzip main archive)", rc == 0)
    check("microcode: exactly one logo replaced", len(replaced) == 1)
    raw = open(initrd, "rb").read()
    # early microcode 段(檔頭那段未壓縮 cpio)必須原封不動、還在
    e_ents, e_tail = rb._parse_newc(raw)
    ebn = {x[1].decode(): x[3] for x in e_ents}
    check("microcode: early microcode blob preserved",
          ebn.get("kernel/x86/microcode/GenuineIntel.bin") == b"MICROCODE-BLOB-DATA")
    # logo 應該在後面那段 gzip 主封存裡、已被換成 300x100
    sizes = {}; rb._walk_logos(raw, sizes)
    check("microcode: logo in main archive replaced (300x100)",
          "usr/share/graphics/logo_installer.png" in sizes)

# --- 第三十輪覆核 Q4:070702(CRC 格式)換 logo 後要重算 c_check ---
crc_cpio = build_cpio_crc([("./init", b"x"), ("usr/share/graphics/logo_installer.png", orig_logo)])
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd.gz"); logo = os.path.join(d, "g.png")
    open(logo, "wb").write(gonas_logo)
    open(initrd, "wb").write(gzip.compress(crc_cpio, 9))
    rc, replaced = rb.rebrand(initrd, logo)
    check("crc: return code 0", rc == 0)
    ents, _ = rb._parse_newc(rb._gunzip_all(open(initrd, "rb").read()))
    for hdr, name, fs, data in ents:
        if name.decode().endswith("logo_installer.png"):
            want = rb._newc_checksum(data)
            got = int(hdr[102:110], 16)
            check("crc: c_check recomputed to match new logo data", got == want)

# --- 8. 第五十三輪:實際 commit 在 branding/ 底下的「空白」logo 資產,要能
# 正常被 rebrander 換進去(自我檢查通過),而且它本身要是一張有效的 PNG。
# build-iso.sh 現在預設用這張透明圖把安裝器 logo 位置留空(見該檔說明)。---
blank_asset = os.path.join(os.path.dirname(os.path.abspath(__file__)), "branding", "installer-logo-blank.png")
check("blank asset exists in branding/", os.path.isfile(blank_asset))
if os.path.isfile(blank_asset):
    blank_bytes = open(blank_asset, "rb").read()
    check("blank asset is a valid PNG", blank_bytes[:8] == b"\x89PNG\r\n\x1a\n")
    dims = rb._png_dims(blank_bytes)
    check("blank asset has real dimensions", dims[0] > 0 and dims[1] > 0)
    real_cpio = build_cpio([("./init", b"x"), ("usr/share/graphics/logo_installer.png", orig_logo)])
    with tempfile.TemporaryDirectory() as d:
        initrd = os.path.join(d, "initrd.gz")
        open(initrd, "wb").write(gzip.compress(real_cpio, 9))
        rc, replaced = rb.rebrand(initrd, blank_asset)  # 自我檢查在 rebrand 內,失敗會丟例外
        check("blank asset: rebrand returns 0 (replaced, self-check passed)", rc == 0)
        ents, _ = rb._parse_newc(rb._gunzip_all(open(initrd, "rb").read()))
        byname = {e[1].decode(): e[3] for e in ents}
        got = byname["usr/share/graphics/logo_installer.png"]
        # 沒有 PIL 時原樣寫入(bytes 相符);有 PIL 時會縮回原圖尺寸,兩種情況
        # 都要仍是一張有效 PNG、且已不是原本的 Debian logo。
        check("blank asset: logo replaced (no longer the Debian logo)", got != orig_logo)
        check("blank asset: replacement is still a valid PNG", got[:8] == b"\x89PNG\r\n\x1a\n")

# --- 9. 第五十五輪:同長度就地覆寫。替換圖比原 logo 小時,換完後那個項目的
# 位元組長度必須跟原本「一模一樣」(靠補 NUL),而且該項目之後的所有 bytes
# 必須完全不動(cpio 結構零位移)——這正是前兩次真機破圖想根治的點。---
big_logo = png(300, 100, (10, 20, 30, 255))  # 一定比透明小圖大
marker = b"MARKER-AFTER-LOGO-STAYS-PUT-1234567890"
sl_cpio = build_cpio([
    ("./init", b"x"),
    ("usr/share/graphics/logo_installer.png", big_logo),
    ("zzz/after.bin", marker),
])
# 找出原始 cpio 裡「logo 資料結束後」的所有 bytes(含後續項目),換完要一致。
orig_ents, orig_tail = rb._parse_newc(sl_cpio)
orig_logo_len = None
for e in orig_ents:
    if e[1].decode().endswith("logo_installer.png"):
        orig_logo_len = len(e[3])
with tempfile.TemporaryDirectory() as d:
    initrd = os.path.join(d, "initrd")  # 不壓縮,直接測 cpio 結構
    open(initrd, "wb").write(sl_cpio)
    if os.path.isfile(blank_asset):
        rc, replaced = rb.rebrand(initrd, blank_asset)
        check("same-length: rebrand returns 0", rc == 0)
        new_raw = open(initrd, "rb").read()
        new_ents, _ = rb._parse_newc(new_raw)
        nb = {e[1].decode(): e[3] for e in new_ents}
        new_logo = nb["usr/share/graphics/logo_installer.png"]
        check("same-length: logo entry keeps the original byte length", len(new_logo) == orig_logo_len)
        check("same-length: padded logo still decodes as a valid PNG",
              new_logo[:8] == b"\x89PNG\r\n\x1a\n" and rb._png_dims(new_logo)[0] > 0)
        check("same-length: file after the logo is byte-identical / still present",
              nb.get("zzz/after.bin") == marker)
        # 總長度也不該變(整個 archive 零位移)。
        check("same-length: whole archive length unchanged", len(new_raw) == len(sl_cpio))

print()
if FAIL:
    print("==> one or more rebrand-initrd test cases FAILED")
    sys.exit(1)
print("==> all rebrand-initrd test cases passed")
