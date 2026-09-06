# deb-closure.sh 把「給定一份 Debian 的 Packages 索引檔,算出某幾個
# 套件(例如 openssh-server)完整的相依封閉集(dependency closure),
# 但排除掉那些 base 系統本來就一定裝好的套件」這段純邏輯,獨立成一個
# 可以離線用假資料測試的函式。
#
# 這是第十九輪覆閱「模式一:離線 SSH」的核心:appliance 想做到「安裝
# 過程完全不連網,但裝完就有 SSH server 可以用」——netinst 光碟本身
# 官方定義就只含「裝 base 系統的最小套件」,openssh-server 這種東西
# 不在裡面,正常安裝流程要連網去鏡像站抓。模式一的做法是:在「建置
# ISO 的機器上(有網路)」先把 openssh-server 以及它需要的所有相依
# 套件的 .deb 檔案抓下來、包進 ISO,之後 late-command.sh 在目標系統裡
# 直接 `dpkg -i` 這些本地檔案,不需要任何網路。
#
# 要抓哪些 .deb,不能只抓 openssh-server 一個——它相依一串其他套件
# (openssh-client、openssh-sftp-server、libssl、libkrb5……)。但也
# 不能把整個相依樹全抓下來(那會包含 libc6 這種 base 一定有的東西,
# 又大又可能在 chroot 裡 dpkg -i 覆蓋掉 base 的敏感套件)。正確的做法
# 是:算出相依封閉集,但排除掉 Priority 是 required/important 的套件
# ——debootstrap 建立的 base 系統一定包含 required + important 這兩個
# 優先級的所有套件(這是 Debian「important 就是預設會裝」的定義),
# 所以那些一定已經在目標系統上、不需要我們再打包一份。
#
# 為什麼要獨立成一個可測試的函式:這段「解析 Packages 格式 + 算封閉集
# + 處理 `|` 替代相依 / 版本限制 `(>= x)` / 架構修飾 `:any` / 虛擬套件
# Provides」的邏輯,是整個模式一裡唯一「不需要真的連網、也不需要真的
# 建 ISO,就能拿假資料驗證對錯」的部分——這個開發沙盒沒辦法真的建
# 一次 ISO、跑一次安裝來端對端驗證,所以把可以離線測的核心邏輯抽出來、
# 寫一支 test-deb-closure.sh 用手寫的假 Packages 資料跑,是目前信心
# 最高的驗證方式。真正下載 .deb、dpkg -i 那些 I/O 步驟沒辦法單元測試,
# 設計成 best-effort(失敗只記 log、不會讓整個 appliance 壞掉)。
#
# 用法:
#   . "$(dirname "$0")/lib/deb-closure.sh"
#   gonas_deb_closure <Packages檔(已解壓成純文字)> <排除的優先級,逗號分隔> <種子套件1> [種子套件2 ...]
#
# 輸出:封閉集裡每個「需要打包」的套件的 Filename 欄位(相對於鏡像站
# 根目錄的 pool/ 路徑),一行一個,印到 stdout;找不到的種子/相依套件
# 印警告到 stderr(不中斷)。
gonas_deb_closure() {
    _gdc_packages="$1"
    _gdc_exclude="$2"
    shift 2
    _gdc_seeds="$*"

    awk -v exclude="$_gdc_exclude" -v seeds="$_gdc_seeds" '
    function trim(s){ sub(/^[ \t]+/,"",s); sub(/[ \t]+$/,"",s); return s }
    # 把一個相依 token(例如 "libc6:any (>= 2.34)")化簡成純套件名 "libc6"
    function canon(dep,   n){
        n=dep
        sub(/\(.*$/,"",n)   # 去掉版本限制 "(>= x)" 之後的所有東西
        sub(/:.*/,"",n)     # 去掉架構修飾 ":any"/":arm64" 之後的所有東西
        gsub(/[ \t]/,"",n)  # 去掉所有空白
        return n
    }
    function flush(   provlist,np,i,pn){
        if(cur_pkg!=""){
            priority[cur_pkg]=cur_prio
            filename[cur_pkg]=cur_file
            depends[cur_pkg]=cur_dep
            predepends[cur_pkg]=cur_predep
            exists[cur_pkg]=1
            if(cur_prov!=""){
                np=split(cur_prov,provlist,",")
                for(i=1;i<=np;i++){
                    pn=canon(provlist[i])
                    # 第一個提供某虛擬套件的實體套件,拿來當該虛擬名稱的解答
                    if(pn!="" && !(pn in provider)) provider[pn]=cur_pkg
                }
            }
        }
        cur_pkg="";cur_prio="";cur_file="";cur_dep="";cur_predep="";cur_prov=""
    }
    # 把一個相依名稱解成「真正存在的實體套件名」:直接存在就用它,
    # 否則看是不是某個套件用 Provides 提供的虛擬套件,再否則回傳空字串
    # (未知/無法滿足的虛擬相依,best-effort 跳過)。
    function resolve(dep,   n){
        n=canon(dep)
        if(n=="") return ""
        if(n in exists) return n
        if(n in provider) return provider[n]
        return ""
    }
    function enqueue_deps(pkg,   field,groups,ng,i,alts,na,r){
        field=depends[pkg]
        if(predepends[pkg]!="") field=field","predepends[pkg]
        ng=split(field,groups,",")
        for(i=1;i<=ng;i++){
            # "a | b | c" 這種替代相依取第一個(apt 在都沒裝的情況下也
            # 是優先選第一個替代方案),已經夠用。
            na=split(groups[i],alts,"|")
            r=resolve(alts[1])
            if(r!="" && !(r in seen)){ qtail++; queue[qtail]=r }
        }
    }
    BEGIN{
        nseed=split(seeds,sa," ")
        for(i=1;i<=nseed;i++){ if(sa[i]!=""){ seedset[sa[i]]=1 } }
        nex=split(exclude,ea,",")
        for(i=1;i<=nex;i++){ if(ea[i]!=""){ exset[ea[i]]=1 } }
    }
    /^$/ { flush(); next }
    {
        idx=index($0,": ")
        if(idx>0){
            key=substr($0,1,idx-1)
            val=substr($0,idx+2)
            if(key=="Package") cur_pkg=trim(val)
            else if(key=="Priority") cur_prio=trim(val)
            else if(key=="Filename") cur_file=trim(val)
            else if(key=="Depends") cur_dep=val
            else if(key=="Pre-Depends") cur_predep=val
            else if(key=="Provides") cur_prov=val
        }
        next
    }
    END{
        flush()   # 檔案結尾如果沒有空行,最後一個 stanza 在這裡收尾
        qhead=0; qtail=0
        for(i=1;i<=nseed;i++){ if(sa[i]!=""){ qtail++; queue[qtail]=sa[i] } }
        while(qhead<qtail){
            qhead++
            pkg=queue[qhead]
            if(pkg in seen) continue
            seen[pkg]=1
            if(!(pkg in exists)){
                print "gonas_deb_closure: warning: package not found in Packages index: " pkg > "/dev/stderr"
                continue
            }
            # 種子套件一律納入;非種子的相依,如果它的優先級屬於「base
            # 一定已經裝好」的排除清單,就整個跳過(不打包、也不往下
            # 展開它的相依——base 是相依封閉的,它的相依也一定在 base 裡)。
            if(!(pkg in seedset) && (priority[pkg] in exset)) continue
            if(filename[pkg]==""){
                print "gonas_deb_closure: warning: no Filename field for " pkg > "/dev/stderr"
                continue
            }
            print filename[pkg]
            enqueue_deps(pkg)
        }
    }
    ' "$_gdc_packages"
}
