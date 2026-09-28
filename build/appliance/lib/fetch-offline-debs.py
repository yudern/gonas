#!/usr/bin/env python3
# fetch-offline-debs.py —— 建置期(在有網路的 Mac 上)把「這片 DVD 上沒有、
# 但 GoNAS 需要的選用套件」連同它們的相依封閉集,從 Debian 官方鏡像下載
# 下來,攤平成一個「flat repo」放進 ISO 的 gonas/debs/,附一份 Packages
# 索引。裝好開機之後,late-command.sh 會把它複製到 /var/lib/gonas/debs/
# 並加一條 `deb [trusted=yes] file:///var/lib/gonas/debs ./` 的本機來源
# ——這樣使用者在 Web 介面點「安裝 mergerfs/samba/...」時,apt 就能「完全
# 離線」從這份本機來源裝好(見 doctor 一鍵安裝的流程),不需要連網。
#
# 為什麼要有這一步(第五十八輪,使用者:「這幾個軟件你為什麼還需要聯網
# 你不做成離線安裝的?」):Debian DVD-1 這片安裝碟「不含」mergerfs /
# snapraid / samba / nfs-kernel-server / wireguard-tools / docker.io(建置時
# 掃 pool/ 已證實,見 build-iso.sh 4.7 節的輸出),所以光靠 DVD 沒辦法
# 離線裝這幾個。唯一能做到「安裝/使用時完全不連網」的辦法,就是在建置期
# (Mac 有網路)先把這些 .deb + 相依一起抓下來塞進 ISO。這也是使用者要的
# 「兩者都要」——內建離線可裝,開機後有網路時 apt 也照樣能走網路補裝更新。
#
# 設計上刻意「不」碰 Debian 自己的套件庫/索引(dists/、Release、pool/ 的
# 索引一律不動)——只新增一個獨立、`[trusted=yes]` 的本機 flat repo。這樣
# 就算這支腳本產生的索引有任何問題,最多是「這幾個選用套件離線裝不起來、
# 退回走網路」,絕不會弄壞「基礎系統安裝」本身。
#
# 相依封閉集的計算(compute_closure)是純資料處理、不碰網路,獨立成函式
# 讓 test-fetch-offline-debs.py 能用假的 Packages 資料離線驗證(這個沙盒
# 連不到 deb.debian.org,真正的下載沒辦法在這裡測,但「給定索引算出要帶
# 哪些套件」這段邏輯測得到)。
#
# 用法:
#   python3 fetch-offline-debs.py \
#       --mirror http://deb.debian.org/debian \
#       --codename trixie --arch amd64 \
#       --out /path/to/iso/gonas/debs \
#       mergerfs snapraid samba nfs-kernel-server wireguard-tools docker.io
#
# 結束碼:0=成功(至少把要求的套件都下載了)、3=網路/鏡像抓不到索引
# (build-iso.sh 會當成 best-effort 警告後繼續,不中斷建置)、1=其他錯誤。

import argparse
import gzip
import hashlib
import io
import os
import sys
import urllib.error
import urllib.request

# 這些「優先權」的套件,視同「基礎系統 + tasksel standard 一定會裝好」,
# 不下載、也不遞迴進去找它們的相依——這是把相依封閉集「收斂」到只剩選用
# 套件那一小撮的關鍵(不然會把 libc6 之類整個 base 系統都拖進來)。preseed
# 有設 `tasksel/first multiselect standard`,所以 standard 這一層確實會裝。
BASE_PRIORITIES = {"required", "important", "standard"}

# 下載/解析時要看的元件(component)。這幾個選用套件主要在 main,但
# docker.io 之類偶有相依落在 contrib;non-free-firmware 併著抓不吃虧。
DEFAULT_COMPONENTS = ["main", "contrib", "non-free", "non-free-firmware"]


def log(msg):
    print("[fetch-offline-debs] " + msg, file=sys.stderr)


def _http_get(url, timeout=60, retries=3):
    """抓一個 URL 的原始 bytes;失敗重試幾次。回傳 bytes 或丟出例外。"""
    last = None
    for attempt in range(1, retries + 1):
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "gonas-build/1"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                return resp.read()
        except (urllib.error.URLError, OSError) as e:  # 含 timeout
            last = e
            log("  fetch attempt %d/%d failed for %s: %s" % (attempt, retries, url, e))
    raise last


def parse_packages(text):
    """把一份 Debian `Packages` 檔的內容(str)解析成 [stanza dict, ...]。
    每個 stanza 是欄位名(原樣大小寫)-> 值(多行值會被接成一行,夠用了,
    我們只需要 Package/Version/Depends/Pre-Depends/Provides/Priority/
    Essential/Filename/Size/SHA256 這些單行欄位)。"""
    stanzas = []
    cur = {}
    last_key = None
    for line in text.split("\n"):
        if line == "":
            if cur:
                stanzas.append(cur)
                cur = {}
                last_key = None
            continue
        if line[0] in (" ", "\t"):
            # 續行:接到上一個欄位後面(對我們用到的欄位其實不重要)。
            if last_key is not None:
                cur[last_key] += " " + line.strip()
            continue
        if ":" in line:
            key, _, val = line.partition(":")
            key = key.strip()
            cur[key] = val.strip()
            last_key = key
    if cur:
        stanzas.append(cur)
    return stanzas


def build_index(stanzas):
    """由 stanzas 建出:
      pkgs: package-name -> stanza(同名多版本時保留「版本較高」那個)
      provides: virtual-name -> [提供它的實體套件名, ...]
    """
    pkgs = {}
    provides = {}
    for st in stanzas:
        name = st.get("Package")
        if not name:
            continue
        prev = pkgs.get(name)
        if prev is None or _version_gt(st.get("Version", ""), prev.get("Version", "")):
            pkgs[name] = st
    # provides 要在確定每個名字的「入選 stanza」之後再建,避免舊版本的
    # Provides 蓋掉新版本。
    for name, st in pkgs.items():
        for prov in _split_provides(st.get("Provides", "")):
            provides.setdefault(prov, [])
            if name not in provides[prov]:
                provides[prov].append(name)
    return pkgs, provides


def _version_gt(a, b):
    """非常粗略的版本比較——只要能在「同一鏡像同一 suite」裡挑出一個穩定的
    代表版本即可(同一 suite 通常一個套件就一個版本,這裡幾乎不會真的用到
    複雜比較)。用 (epoch, 原字串) 的樸素比較,夠穩定、不追求 dpkg 精確語意。"""
    if b == "":
        return True
    if a == "":
        return False
    return a > b


def _split_deps(field):
    """把 Depends/Pre-Depends 欄位切成 [[alt1, alt2], ...] 的結構;每個 alt
    是去掉版本限制 `(>= x)`、架構限制 `[amd64]`、build-profile `<...>` 之後的
    純套件名。"""
    groups = []
    if not field:
        return groups
    for part in field.split(","):
        alts = []
        for alt in part.split("|"):
            name = alt.strip()
            # 砍掉 (版本)、[架構]、<profile>
            for cut in ("(", "[", "<"):
                idx = name.find(cut)
                if idx != -1:
                    name = name[:idx]
            name = name.strip()
            if name:
                alts.append(name)
        if alts:
            groups.append(alts)
    return groups


def _split_provides(field):
    out = []
    if not field:
        return out
    for part in field.split(","):
        name = part.strip()
        idx = name.find("(")
        if idx != -1:
            name = name[:idx].strip()
        if name:
            out.append(name)
    return out


def _is_base(st):
    """這個套件是否視同「基礎系統一定有」(不需要下載/遞迴)。"""
    if st.get("Essential", "").lower() == "yes":
        return True
    if st.get("Priority", "").lower() in BASE_PRIORITIES:
        return True
    return False


def _resolve_alt(alts, pkgs, provides):
    """給一組相依 alternatives(a|b|c),挑一個「要納入封閉集」的實體套件名;
    挑不到(全都不在索引、也沒有提供者)就回 None。挑選規則:
      1) 先找「是實體套件」的 alt(優先)。
      2) 否則找「有實體提供者(Provides)」的 alt,取第一個提供者。
    回傳 (chosen_pkg_name, satisfied_by_base_bool)。"""
    # 第一輪:實體套件
    for a in alts:
        st = pkgs.get(a)
        if st is not None:
            return a, _is_base(st)
    # 第二輪:虛擬套件 -> 提供者
    for a in alts:
        prov = provides.get(a)
        if prov:
            chosen = prov[0]
            st = pkgs.get(chosen)
            return chosen, (_is_base(st) if st else False)
    return None, False


def compute_closure(pkgs, provides, targets):
    """從 targets(要離線可裝的選用套件名)出發,沿 Depends + Pre-Depends
    展開相依封閉集,遇到「基礎系統一定有」(_is_base)的套件就當作已滿足、
    不納入也不再往下展開。回傳「需要下載打包」的套件名集合(含 targets 本身
    ——就算某個 target 剛好是 base 優先權,使用者要的就是它,一定納入)。

    純資料處理、不碰網路,方便離線單元測試。"""
    needed = set()
    missing_targets = []
    queue = []
    for t in targets:
        if t not in pkgs:
            missing_targets.append(t)
            continue
        needed.add(t)
        queue.append(t)
    while queue:
        name = queue.pop()
        st = pkgs.get(name)
        if st is None:
            continue
        dep_field = (st.get("Pre-Depends", "") + ", " + st.get("Depends", "")).strip(", ")
        for alts in _split_deps(dep_field):
            chosen, by_base = _resolve_alt(alts, pkgs, provides)
            if chosen is None:
                continue
            if by_base:
                continue  # 基礎系統會有,不打包
            if chosen not in needed:
                needed.add(chosen)
                queue.append(chosen)
    return needed, missing_targets


def prune_unsatisfiable(pkgs, provides, available):
    """反覆剔除「有某個相依群組無法被 (base 或 available 裡的套件) 滿足」的
    套件,直到穩定,回傳可安全放進本機 repo 的套件名集合。

    available 是「實際下載成功」的套件名集合。某個相依 .deb 下載失敗(不在
    available)時,任何仍依賴它的套件都必須從 bundle 移除——否則離線 apt
    install 會因未滿足相依而失敗。純資料處理、不碰網路,方便離線單元測試。"""
    avail = set(available)
    changed = True
    while changed:
        changed = False
        for name in list(avail):
            st = pkgs.get(name)
            if st is None:
                avail.discard(name)
                changed = True
                continue
            dep_field = (st.get("Pre-Depends", "") + ", " + st.get("Depends", "")).strip(", ")
            ok = True
            for alts in _split_deps(dep_field):
                grp_ok = False
                for a in alts:
                    chosen, by_base = _resolve_alt([a], pkgs, provides)
                    if by_base or (chosen is not None and chosen in avail):
                        grp_ok = True
                        break
                if not grp_ok:
                    ok = False
                    break
            if not ok:
                avail.discard(name)
                changed = True
    return avail


def _sha256(data):
    h = hashlib.sha256()
    h.update(data)
    return h.hexdigest()


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("--mirror", default="http://deb.debian.org/debian")
    ap.add_argument("--codename", required=True)
    ap.add_argument("--arch", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--components", default=",".join(DEFAULT_COMPONENTS))
    ap.add_argument("packages", nargs="+")
    args = ap.parse_args(argv)

    # --mirror 可以是「逗號分隔的多個鏡像」,依序嘗試,第一個「連得到、抓得到
    # 索引」的就用它做後續所有 .deb 下載(確保版本一致)。第五十九輪加:中國大陸
    # 常常連不到 deb.debian.org,自動退到 tuna/ustc,使用者不必自己知道要設環境
    # 變數。(仍可用 GONAS_DEB_MIRROR 覆寫。)
    mirrors = [m.strip().rstrip("/") for m in args.mirror.split(",") if m.strip()]
    components = [c for c in args.components.split(",") if c]
    targets = list(dict.fromkeys(args.packages))  # 去重、保序

    # 1) 依序試每個鏡像,抓各 component 的 Packages.gz;第一個成功的鏡像就定案。
    all_stanzas = []
    mirror = None
    for cand in mirrors:
        stanzas_this = []
        got = False
        for comp in components:
            url = "%s/dists/%s/%s/binary-%s/Packages.gz" % (cand, args.codename, comp, args.arch)
            try:
                raw = _http_get(url, timeout=30, retries=2)
            except Exception as e:
                log("  [%s] component %s unreachable (%s)" % (cand, comp, e))
                continue
            try:
                text = gzip.decompress(raw).decode("utf-8", "replace")
            except Exception as e:
                log("  [%s] could not gunzip %s: %s" % (cand, comp, e))
                continue
            st = parse_packages(text)
            log("  [%s] component %s: %d package stanzas" % (cand, comp, len(st)))
            stanzas_this.extend(st)
            got = True
        if got:
            mirror = cand
            all_stanzas = stanzas_this
            log("using mirror for offline .debs: %s" % mirror)
            break
        log("mirror %s did not work, trying the next one…" % cand)

    if mirror is None:
        log("ERROR: could not fetch a package index from ANY mirror tried (%s) for %s/%s. "
            "Offline packages will NOT be bundled; they will need network at install time. "
            "If you are behind a filtered network, pass a reachable mirror with "
            "GONAS_DEB_MIRROR=<url> make iso-amd64." % (", ".join(mirrors), args.codename, args.arch))
        return 3

    pkgs, provides = build_index(all_stanzas)
    log("merged index: %d unique packages" % len(pkgs))

    # 2) 算相依封閉集。
    needed, missing_targets = compute_closure(pkgs, provides, targets)
    if missing_targets:
        log("WARNING: these requested packages are not in the mirror index and will be skipped: %s"
            % " ".join(missing_targets))
    log("dependency closure: %d packages to bundle" % len(needed))

    # 3) 下載每個 .deb,攤平放進 out/。先全部下載,記錄「真的下載成功」的套件。
    os.makedirs(args.out, exist_ok=True)
    stanza_by_name = {}   # name -> (basename, stanza)
    downloaded_names = set()
    total_bytes = 0
    # 依名稱排序,輸出穩定、方便 diff/除錯。
    for name in sorted(needed):
        st = pkgs[name]
        filename = st.get("Filename")
        if not filename:
            log("WARNING: %s has no Filename in index — skipping" % name)
            continue
        base = os.path.basename(filename)
        dest = os.path.join(args.out, base)
        url = "%s/%s" % (mirror, filename)
        try:
            data = _http_get(url)
        except Exception as e:
            log("WARNING: failed to download %s (%s)" % (url, e))
            continue
        # 有 SHA256 就核對,抓到壞檔早點發現(apt 之後也會核對)。
        want = st.get("SHA256")
        if want and _sha256(data) != want:
            log("WARNING: SHA256 mismatch for %s — skipping (corrupt download?)" % base)
            continue
        with open(dest, "wb") as f:
            f.write(data)
        downloaded_names.add(name)
        total_bytes += len(data)
        stanza_by_name[name] = (base, st)

    if not downloaded_names:
        log("ERROR: downloaded 0 packages — nothing to bundle.")
        return 3

    # 3.5) 第五十八輪建置覆核(#2):若有下載失敗,反覆剔除「相依解不開」的
    # 套件,避免 Packages 裡留下「指向缺檔相依」的套件——那會讓離線 apt
    # install 因未滿足相依而整個失敗。剔到穩定為止,只保留每個相依都能被
    # (base 或已下載套件)滿足的套件。
    satisfiable = prune_unsatisfiable(pkgs, provides, downloaded_names)
    dropped = downloaded_names - satisfiable
    if dropped:
        log("WARNING: dropping %d package(s) from the offline bundle because a dependency could not be downloaded: %s"
            % (len(dropped), " ".join(sorted(dropped))))
    dropped_targets = [t for t in targets if t in pkgs and t not in satisfiable]
    if dropped_targets:
        log("WARNING: these requested packages will NOT be offline-installable (a dependency was missing) — they will need network: %s"
            % " ".join(dropped_targets))
    if not satisfiable:
        log("ERROR: no package remained fully satisfiable after pruning — not writing a bundle.")
        return 3

    # 4) 只為「相依都齊全」的套件寫 flat repo 的 Packages(stanza 之間空一行,
    # 結尾留一空行)。沿用鏡像原本的所有欄位(含 Size/SHA256/MD5sum,apt 會
    # 拿來核對),只把 Filename 改寫成相對本機 repo 根目錄的 `./basename`。
    packages_out = []
    for name in sorted(satisfiable):
        base, st = stanza_by_name[name]
        new_lines = []
        for key, val in st.items():
            if key == "Filename":
                new_lines.append("Filename: ./%s" % base)
            else:
                new_lines.append("%s: %s" % (key, val))
        packages_out.append("\n".join(new_lines))

    packages_path = os.path.join(args.out, "Packages")
    with open(packages_path, "w", encoding="utf-8") as f:
        f.write("\n\n".join(packages_out))
        f.write("\n")
    log("wrote %s (%d packages, %.1f MiB downloaded)" % (packages_path, len(packages_out), total_bytes / 1048576.0))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
