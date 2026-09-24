#!/usr/bin/env python3
# rebrand-installer-initrd.py — 把 debian-installer 圖形(gtk)安裝程式
# initrd 裡的 Debian 標誌圖片,原地換成 GoNAS 標誌。
#
# 為什麼是這支腳本、而且是純 Python:
#   * 圖形安裝器畫面正上方那張最顯眼的 Debian logo,是一個 PNG 檔
#     (通常在 initrd 裡的 usr/share/graphics/logo_installer.png),
#     要換掉它,唯一的辦法就是把 initrd 拆開、換檔、再打包回去。
#   * 建置機(使用者的 Mac)一定有 gzip;cpio 也是 macOS 內建;但這支
#     腳本刻意「不」依賴 cpio 指令,改用純 Python 自己解析/產生 newc
#     cpio 格式——理由是這樣才能在完全沒有 cpio 的開發沙盒裡,用合成的
#     假 initrd 把「拆-換-打包」這條邏輯本身完整跑測試(見
#     test-rebrand-installer-initrd.py),不會出現「測試跟正式跑的是
#     兩份不同程式碼」的老問題。Python3 在裝了 Xcode Command Line
#     Tools 的 Mac 上一定有(brew/git/make 能動就代表 CLT 裝好了)。
#   * 壓縮解壓用 Python 標準函式庫的 gzip / lzma(xz),不需要外部工具;
#     debian-installer 的 initrd.gz 慣例上就是單一 gzip 串流。
#
# 誠實邊界:這支腳本只換得動「圖片」。圖形安裝器每個畫面標題那些寫著
# "Debian" 的「文字」,是編譯進安裝程式 cdebconf 模板/翻譯檔裡的,散在
# 幾十個 udeb / 語言檔,不重建整個 debian-installer 改不掉——那是另一個
# 層級的工程(見 build/appliance/README.md 的說明)。
#
# 用法:
#   python3 rebrand-installer-initrd.py <initrd檔> <gonas_logo.png>
# 回傳碼:
#   0 = 有換到至少一張 logo(已寫回)
#   2 = 這個 initrd 裡找不到可換的 logo(沒有修改,呼叫端當 warning)
#   1 = 出錯(格式不認得/寫檔失敗等)

import sys, os, gzip, lzma, struct

# initrd 裡「要被換掉」的 logo 檔案:用「路徑結尾比對」而不是完整路徑,
# 這樣不管前面掛載點/相對路徑怎麼寫都認得。只鎖定 graphics 目錄底下、
# 檔名明確是安裝器 logo 的那幾個,不會誤傷其他 PNG。
LOGO_SUFFIXES = (
    "usr/share/graphics/logo_installer.png",
    "usr/share/graphics/logo_debian.png",
    "/logo_installer.png",
    "/logo_debian.png",
)

NEWC_MAGIC = b"070701"
NEWC_CRC_MAGIC = b"070702"


def _detect_and_decompress(raw):
    """回傳 (cpio_bytes, recompress_fn)。recompress_fn(bytes)->bytes 用
    同一種壓縮法把資料壓回去。認得 gzip / xz / 未壓縮的裸 cpio。"""
    if raw[:2] == b"\x1f\x8b":                       # gzip
        # 用 zlib 逐個 member 解,涵蓋「多個 gzip 串流串接」的情況。
        data = _gunzip_all(raw)
        return data, lambda b: gzip.compress(b, 9)
    if raw[:6] == b"\xfd7zXZ\x00":                   # xz
        return lzma.decompress(raw), lambda b: lzma.compress(b, preset=6)
    if raw[:6] in (NEWC_MAGIC, NEWC_CRC_MAGIC):      # 裸 cpio(未壓縮)
        return raw, lambda b: b
    raise ValueError("unrecognised initrd container (not gzip/xz/newc-cpio)")


def _gunzip_all(raw):
    import zlib
    out = bytearray()
    i, n = 0, len(raw)
    while i < n:
        if raw[i : i + 2] != b"\x1f\x8b":
            break
        d = zlib.decompressobj(16 + zlib.MAX_WBITS)
        out += d.decompress(raw[i:])
        out += d.flush()
        consumed = n - i - len(d.unused_data)
        i += consumed
    return bytes(out)


def _parse_newc(data):
    """把 newc cpio 拆成 [(header110:bytes, name:bytes, filesize:int,
    filedata:bytes)],外加 trailer 之後殘留的尾端 bytes(原樣保留)。"""
    entries = []
    off = 0
    n = len(data)
    while off + 110 <= n:
        header = data[off : off + 110]
        if header[:6] not in (NEWC_MAGIC, NEWC_CRC_MAGIC):
            raise ValueError("bad cpio header magic at offset %d" % off)
        namesize = int(header[94:102], 16)
        filesize = int(header[54:62], 16)
        name_off = off + 110
        name = data[name_off : name_off + namesize]
        # header(110)+name 之後補 NUL 到 4 的倍數
        after_name = 110 + namesize
        name_pad = (-after_name) % 4
        data_off = name_off + namesize + name_pad
        filedata = data[data_off : data_off + filesize]
        data_pad = (-filesize) % 4
        nxt = data_off + filesize + data_pad
        clean_name = name[:-1] if name.endswith(b"\x00") else name
        entries.append([header, clean_name, filesize, filedata])
        off = nxt
        if clean_name == b"TRAILER!!!":
            break
    tail = data[off:]  # trailer 之後的填塞/第二段 archive,原樣保留
    return entries, tail


def _newc_checksum(data):
    """newc「CRC」格式(magic 070702)的 c_check 欄位定義:所有檔案資料位元組
    相加後對 2^32 取模(不是真的 CRC)。換掉 logo 資料後要重算,否則會產出
    checksum 對不上的壞封存(第三十輪覆核 Q4)。"""
    return sum(data) & 0xFFFFFFFF


def _emit_entries(entries):
    """把一串 entry 重新序列化成一段 newc cpio(不含尾端 tail)。只改寫會變動
    的欄位(filesize;CRC 格式再加 c_check),其餘 header 位元組原封不動,確保
    沒被改到的項目 byte-for-byte 一模一樣。"""
    out = bytearray()
    for header, clean_name, filesize, filedata in entries:
        name = clean_name + b"\x00"
        namesize = len(name)
        new_fs = len(filedata)
        fs_field = header[54:62]
        fmt = ("%08x" if fs_field == fs_field.lower() else "%08X")
        patched = bytearray(header)
        patched[54:62] = (fmt % new_fs).encode("ascii")
        if bytes(header[:6]) == NEWC_CRC_MAGIC:
            # c_check 是 header 最後 8 個字元(位元組 102:110),不是 100:110
            # ——寫錯位置會踩壞前一個 namesize 欄位、還會改變 header 長度。
            patched[102:110] = (fmt % _newc_checksum(filedata)).encode("ascii")
        out += bytes(patched)
        out += name
        out += b"\x00" * ((-(110 + namesize)) % 4)
        out += filedata
        out += b"\x00" * ((-new_fs) % 4)
    return bytes(out)


def _emit_newc(entries, tail):
    # 薄包裝:entries + 原樣 tail。保留給既有測試(byte-identical round-trip)。
    return _emit_entries(entries) + tail


def _maybe_resize(png_bytes, target_w, target_h):
    """如果建置機剛好有 PIL,就把 GoNAS logo 縮放成原圖尺寸(讓 gtk 版面
    不跑掉);沒有 PIL 就原樣用(尺寸可能跟原圖不同,頂多版面位移,不會
    讓安裝器開不了機)。"""
    if not target_w or not target_h:
        return png_bytes
    try:
        import io
        from PIL import Image
    except Exception:
        return png_bytes
    try:
        im = Image.open(io.BytesIO(png_bytes)).convert("RGBA")
        if im.size == (target_w, target_h):
            return png_bytes
        im = im.resize((target_w, target_h), Image.LANCZOS)
        buf = io.BytesIO()
        im.save(buf, format="PNG")
        return buf.getvalue()
    except Exception:
        return png_bytes


def _png_dims(b):
    # PNG IHDR:8 byte signature + 4 len + 4 "IHDR" + width(4) + height(4)
    if len(b) >= 24 and b[:8] == b"\x89PNG\r\n\x1a\n" and b[12:16] == b"IHDR":
        return struct.unpack(">II", b[16:24])
    return (0, 0)


def _is_logo(clean_name):
    return any(clean_name.endswith(suf) for suf in LOGO_SUFFIXES)


def _replace_in_entries(entries, gonas_png):
    """就地把 entries 裡的 logo 換成新的 PNG,回傳被換掉的清單(含原/新尺寸與
    新位元組長度,供自我檢查與回報用)。

    第五十五輪(真機第三次):前兩次(換 GoNAS logo、換透明圖)在使用者的
    真實映像上都變成破圖,而且是「不管換什麼內容都破」——代表問題出在「換掉
    的檔案長度跟原本不一樣,導致後面的 cpio 項目位移/重新對齊」這條路上,不是
    圖的內容。這裡改成「同長度就地覆寫」:把新 PNG 補上結尾的 NUL bytes,湊到
    跟原檔一模一樣的位元組長度(PNG 解碼器遇到 IEND 就停,結尾多餘的 bytes 會
    被忽略,圖仍然有效)。這樣 filesize 欄位不變、該項目後面的所有 bytes 位置
    完全不動,對整個 cpio 結構等於零改動,只有這個檔案的資料區換了內容。
    只有在新 PNG 比原檔還大(裝不下)時,才退回舊的「換長度」行為。"""
    replaced = []
    for e in entries:
        clean_name = e[1].decode("latin-1")
        if _is_logo(clean_name):
            ow, oh = _png_dims(e[3])
            orig_len = len(e[3])
            new_png = _maybe_resize(gonas_png, ow, oh)
            if len(new_png) <= orig_len:
                # 同長度就地覆寫:補 NUL 到原長度,結構零位移。
                new_png = new_png + b"\x00" * (orig_len - len(new_png))
            nw, nh = _png_dims(new_png)
            e[3] = new_png
            replaced.append((clean_name, ow, oh, nw, nh, len(new_png)))
    return replaced


def _split_padding(tail):
    """把一段 tail 拆成「開頭的 NUL 對齊填塞」與「其餘」。cpio archive 之間、
    以及 archive 到下一段壓縮串流之間,慣例用 NUL 對齊,要原樣保留。"""
    i = 0
    while i < len(tail) and tail[i] == 0:
        i += 1
    return tail[:i], tail[i:]


def _process_segment(blob, gonas_png):
    """處理「一段」initrd:可能是 gzip/xz 容器,或一段(可能後面還接著其他段的)
    newc cpio。回傳 (新的位元組, 被換掉的 logo 清單)。

    第三十輪覆核 Q1:真實 x86 initrd 常是「未壓縮的 early-cpio(microcode)
    ++ 壓縮的主封存」串接;舊版只解析第一段就把其餘丟進 tail 不看,會靜默
    漏掉主封存裡的 logo。這裡改成遞迴處理每一段,microcode 那種串接也涵蓋。"""
    if blob[:2] == b"\x1f\x8b":                       # gzip 容器
        inner, rep = _process_segment(_gunzip_all(blob), gonas_png)
        return gzip.compress(inner, 9), rep
    if blob[:6] == b"\xfd7zXZ\x00":                   # xz 容器
        inner, rep = _process_segment(lzma.decompress(blob), gonas_png)
        return lzma.compress(inner, preset=6), rep
    if blob[:6] in (NEWC_MAGIC, NEWC_CRC_MAGIC):      # 一段 newc cpio
        entries, tail = _parse_newc(blob)
        replaced = _replace_in_entries(entries, gonas_png)
        head = _emit_entries(entries)
        lead, rest = _split_padding(tail)
        if rest:
            new_rest, rep2 = _process_segment(rest, gonas_png)
            return head + lead + new_rest, replaced + rep2
        return head + tail, replaced
    # 認不得的區段:原樣保留,不動也不算失敗。
    return blob, []


def _walk_logos(blob, out):
    """唯讀走訪每一段,把所有 logo 檔目前的位元組長度收進 out(給自我檢查用)。"""
    if blob[:2] == b"\x1f\x8b":
        _walk_logos(_gunzip_all(blob), out); return
    if blob[:6] == b"\xfd7zXZ\x00":
        _walk_logos(lzma.decompress(blob), out); return
    if blob[:6] in (NEWC_MAGIC, NEWC_CRC_MAGIC):
        entries, tail = _parse_newc(blob)
        for e in entries:
            nm = e[1].decode("latin-1")
            if _is_logo(nm):
                out[nm] = len(e[3])
        _, rest = _split_padding(tail)
        if rest:
            _walk_logos(rest, out)


def rebrand(initrd_path, logo_path):
    with open(logo_path, "rb") as f:
        gonas_png = f.read()
    with open(initrd_path, "rb") as f:
        raw = f.read()

    new_raw, replaced = _process_segment(raw, gonas_png)
    if not replaced:
        return 2, []

    # 自我檢查(第三十輪覆核 Q2/Q3:舊的檢查邏輯寫死永遠不觸發、又比錯對象)。
    # 把產生出來的檔案整個再走一遍,確認每個換過的 logo 現在的位元組長度,
    # 就等於我們實際寫進去的新 PNG 長度——表頭 filesize/padding 寫壞會被抓到。
    sizes = {}
    _walk_logos(new_raw, sizes)
    for clean_name, ow, oh, nw, nh, newlen in replaced:
        if sizes.get(clean_name) != newlen:
            raise ValueError(
                "self-check failed for %s: expected %d bytes after repack, got %r"
                % (clean_name, newlen, sizes.get(clean_name))
            )

    # 原子寫回:先寫暫存檔再 rename,避免中途失敗留下半個壞 initrd。
    tmp = initrd_path + ".gonas.tmp"
    with open(tmp, "wb") as f:
        f.write(new_raw)
    os.replace(tmp, initrd_path)
    return 0, replaced


def main(argv):
    if len(argv) != 3:
        sys.stderr.write("usage: rebrand-installer-initrd.py <initrd> <logo.png>\n")
        return 1
    initrd_path, logo_path = argv[1], argv[2]
    try:
        rc, replaced = rebrand(initrd_path, logo_path)
    except Exception as ex:  # noqa: BLE001 — 建置腳本要清楚知道哪裡爆
        sys.stderr.write("error: %s: %s\n" % (os.path.basename(initrd_path), ex))
        return 1
    if rc == 2:
        sys.stderr.write(
            "note: no installer logo found inside %s (left unchanged)\n"
            % os.path.basename(initrd_path)
        )
        return 2
    for clean_name, ow, oh, nw, nh, newlen in replaced:
        sys.stderr.write(
            "    - replaced %s (was %dx%d, now %dx%d, %d bytes)\n"
            % (clean_name, ow, oh, nw, nh, newlen)
        )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
