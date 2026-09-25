#!/usr/bin/env python3
# test-fetch-offline-debs.py —— lib/fetch-offline-debs.py 裡「相依封閉集
# 計算」那段純資料邏輯的離線回歸測試。完全不碰網路(這個沙盒連不到
# deb.debian.org,真正的下載沒辦法在這裡測;但「給定一份 Packages 索引,
# 算出到底要打包哪些套件」這段邏輯,拿手寫的假索引就能完整驗證)。
#
# 用法:  python3 build/appliance/test-fetch-offline-debs.py
# 全過用 exit 0,任一失敗 exit 1。

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "lib"))
import importlib.util

_spec = importlib.util.spec_from_file_location(
    "fetch_offline_debs", os.path.join(os.path.dirname(__file__), "lib", "fetch-offline-debs.py")
)
m = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(m)

FAIL = 0


def check(name, cond):
    global FAIL
    if cond:
        print("PASS: " + name)
    else:
        print("FAIL: " + name, file=sys.stderr)
        FAIL = 1


# 一份手寫的假 Packages:模擬真實情境——
#   mergerfs(optional)-> libfuse3-3(optional,要打包)、libc6(required,base,不打包)
#   samba(optional)   -> samba-common(optional,要打包)、libc6(base)
#   samba-common(optional)-> 無額外相依
#   docker.io(optional)-> containerd(optional,要打包) | 無 => 取 containerd
#                        -> mail-transport-agent(虛擬,由 exim4 提供)=> 打包 exim4
#   containerd(optional)-> runc(optional,要打包)
#   runc(optional)     -> libc6(base)
#   exim4(optional)    -> libc6(base)
#   一個 alternative:foo(optional)-> libc6(base) | somethingelse(optional)
#                     第一個 alt 是 base,應被視為已滿足,不打包 somethingelse。
FAKE_PACKAGES = """\
Package: mergerfs
Version: 2.33.5-1
Priority: optional
Depends: libfuse3-3 (>= 3.10.5), libc6 (>= 2.34)
Filename: pool/main/m/mergerfs/mergerfs_2.33.5-1_amd64.deb
SHA256: aaaa
Size: 100

Package: libfuse3-3
Version: 3.14.0-4
Priority: optional
Depends: libc6 (>= 2.34)
Filename: pool/main/f/fuse3/libfuse3-3_3.14.0-4_amd64.deb
SHA256: bbbb
Size: 50

Package: libc6
Version: 2.38-1
Priority: required
Filename: pool/main/g/glibc/libc6_2.38-1_amd64.deb
SHA256: cccc
Size: 3000

Package: samba
Version: 4.19-1
Priority: optional
Depends: samba-common (= 4.19-1), libc6
Filename: pool/main/s/samba/samba_4.19-1_amd64.deb
SHA256: dddd
Size: 900

Package: samba-common
Version: 4.19-1
Priority: optional
Filename: pool/main/s/samba/samba-common_4.19-1_all.deb
SHA256: eeee
Size: 200

Package: docker.io
Version: 20.10-1
Priority: optional
Depends: containerd, mail-transport-agent
Filename: pool/main/d/docker.io/docker.io_20.10-1_amd64.deb
SHA256: ffff
Size: 5000

Package: containerd
Version: 1.6-1
Priority: optional
Depends: runc (>= 1.0)
Filename: pool/main/c/containerd/containerd_1.6-1_amd64.deb
SHA256: 1111
Size: 400

Package: runc
Version: 1.1-1
Priority: optional
Depends: libc6
Filename: pool/main/r/runc/runc_1.1-1_amd64.deb
SHA256: 2222
Size: 300

Package: exim4
Version: 4.96-1
Priority: optional
Provides: mail-transport-agent
Depends: libc6
Filename: pool/main/e/exim4/exim4_4.96-1_amd64.deb
SHA256: 3333
Size: 150

Package: foo
Version: 1-1
Priority: optional
Depends: libc6 | somethingelse
Filename: pool/main/f/foo/foo_1-1_amd64.deb
SHA256: 4444
Size: 10

Package: somethingelse
Version: 1-1
Priority: optional
Filename: pool/main/s/somethingelse/somethingelse_1-1_amd64.deb
SHA256: 5555
Size: 10
"""

stanzas = m.parse_packages(FAKE_PACKAGES)
check("parse_packages found all 11 stanzas", len(stanzas) == 11)

pkgs, provides = m.build_index(stanzas)
check("build_index has mergerfs", "mergerfs" in pkgs)
check("provides maps mail-transport-agent -> exim4", provides.get("mail-transport-agent") == ["exim4"])

# --- mergerfs 封閉集:要有 mergerfs + libfuse3-3,不能有 libc6(base) ---
needed, missing = m.compute_closure(pkgs, provides, ["mergerfs"])
check("mergerfs closure includes mergerfs", "mergerfs" in needed)
check("mergerfs closure includes libfuse3-3 (optional dep)", "libfuse3-3" in needed)
check("mergerfs closure EXCLUDES libc6 (required/base)", "libc6" not in needed)

# --- samba 封閉集:samba + samba-common,不含 libc6 ---
needed, _ = m.compute_closure(pkgs, provides, ["samba"])
check("samba closure includes samba-common", "samba-common" in needed)
check("samba closure EXCLUDES libc6", "libc6" not in needed)

# --- docker.io 封閉集:遞迴 containerd->runc,虛擬相依 mail-transport-agent
#     由 exim4 滿足,libc6 一律排除 ---
needed, _ = m.compute_closure(pkgs, provides, ["docker.io"])
check("docker closure includes containerd (dep)", "containerd" in needed)
check("docker closure includes runc (transitive dep)", "runc" in needed)
check("docker closure resolves virtual dep via provider exim4", "exim4" in needed)
check("docker closure EXCLUDES libc6", "libc6" not in needed)

# --- alternatives:第一個 alt 是 base(libc6),應視為已滿足,不打包
#     第二個 alt(somethingelse)---
needed, _ = m.compute_closure(pkgs, provides, ["foo"])
check("foo included itself", "foo" in needed)
check("alternative satisfied by base libc6 -> somethingelse NOT bundled",
      "somethingelse" not in needed)

# --- 要求一個索引裡沒有的套件:回報在 missing、不炸 ---
needed, missing = m.compute_closure(pkgs, provides, ["mergerfs", "does-not-exist"])
check("missing target reported", "does-not-exist" in missing)
check("existing target still resolved alongside a missing one", "mergerfs" in needed)

# --- Filename 改寫檢查(模擬 main() 產生 stanza 的那段)---
st = pkgs["mergerfs"]
base = os.path.basename(st["Filename"])
check("basename extracted from pool Filename", base == "mergerfs_2.33.5-1_amd64.deb")

print()
if FAIL == 0:
    print("==> all fetch-offline-debs test cases passed")
    sys.exit(0)
else:
    print("==> fetch-offline-debs tests FAILED", file=sys.stderr)
    sys.exit(1)
