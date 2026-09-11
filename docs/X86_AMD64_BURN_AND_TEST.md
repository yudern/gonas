# x86 (amd64) 專屬:在 Mac mini 上編譯 + 燒錄真機測試

這份文件只講一件事:你人在 Mac mini 前面、要編出 `amd64` 那顆映像檔、
燒到 USB、拿去真正的 x86 機器上開機測試。跟你已經測過的 `arm64`
(Parallels)完全是兩個獨立的產出物,不能混用,細節見下面步驟 0。

完整背景/所有已知限制在 `build/appliance/README.md`;逐步操作的另一份
文件 `APPLIANCE_BUILD_AND_TEST_PROCEDURE.md` 兩個架構都涵蓋、比較長;
這份是專門把「x86 真機」這條路徑抽出來,跳過 Parallels/QEMU 虛擬機
測試,直接給你「編 → 燒 → 開真機」的最短路徑。

## 步驟 0:先搞清楚 amd64 跟 arm64 是兩個不同檔案

你在 Mac mini 上已經編過、測過的 `gonas-<version>-arm64.iso`,**不能**
拿去 x86 機器上開機——那台機器會直接開不了機,或行為不可預期。原因:

| | arm64(你已測過) | amd64(這份文件要做的) |
|---|---|---|
| 來源 Debian ISO | `debian-13.x-arm64-DVD-1.iso`(~3.7GB) | `debian-13.x-amd64-DVD-1.iso`(~3.7GB) |
| 內嵌的 gonasd | `dist/gonasd-linux-arm64` | `dist/gonasd-linux-amd64` |
| 建置指令 | `make iso-arm64` | `make iso-amd64` |
| SSH/sudo 怎麼來 | preseed `pkgsel` 從 DVD-1 離線裝 | preseed `pkgsel` 從 DVD-1 離線裝 |
| 開機模式 | 純 UEFI | BIOS + UEFI 都支援 |

> 註:第二十八輪起底層 ISO 從 netinst(~700MB)換成 DVD-1 完整版
> (~3.7GB)——openssh-server/sudo 直接在 DVD 的套件庫裡,由 preseed
> 的 `pkgsel/include` 離線裝好,不再需要之前那套自己打包 .deb 的做法。
> 代價是映像大 5 倍、下載/燒錄/build 都更久、也更吃磁碟。

腳本邏輯(`build-iso.sh`/`preseed.cfg`/`late-command.sh`)兩邊共用同一份
原始碼,靠 `$ARCH` 參數切換——但**產出的 `.iso` 檔案本身是兩個完全獨立、
互不相容的二進位檔**,一定要各自單獨建置。

## 步驟 1:在 Mac mini 上編出 amd64 映像檔

跟你上次編 arm64 用的是同一台 Mac mini、同一份程式碼,差別只有指令換成
`iso-amd64`。這一步**需要網路**(要下載官方 Debian amd64 DVD-1 ISO,
~3.7GB,第一次會相當慢;建議這台 Mac 至少留 ~12GB 可用空間)。

```
cd gonas
git log --oneline -1   # 確認是最新的 commit(第二十八輪或更新,訊息含 DVD-1)
make iso-amd64
```

過程跟你上次做 arm64 完全一樣(下載 → 驗證雜湊 → 解開 → 塞 gonasd/腳本 →
改開機選單 → xorriso 重新包裝),只是這次抓的是 amd64 版本的官方 ISO,
會需要重新下載一次(不會重用 arm64 那份快取,兩個架構的快取檔案分開存放
在 `dist/.cache/debian-iso/`)。順利的話最後會看到:

```
==> done: dist/release/gonas-<version>-amd64.iso
```

如果卡住或報錯,把完整訊息帶回來——跟之前處理 arm64 bug 一樣的流程。

## 步驟 2:(建議但非必要)先在 Mac mini 上用 QEMU 驗證一次

Mac mini 是 Apple Silicon(arm64 主機),跑 amd64 虛擬機**沒有硬體加速**
(HVF 只加速跟主機同架構的 VM),完全靠軟體模擬,開機/安裝過程可能要等
數十分鐘甚至更久。這一步的意義只是「先在自己機器上抓一次明顯的邏輯
bug」,不是必要步驟——你之前 arm64 已經測過完整的 preseed/late-command
流程,邏輯兩邊共用,理論上不需要重測一次。**如果你想節省時間,直接跳到
步驟 3 燒真機**也完全合理。

真的想先跑一次的話:

```
qemu-img create -f qcow2 gonas-test-disk-amd64.img 20G
qemu-system-x86_64 \
    -m 2048 \
    -cdrom dist/release/gonas-<version>-amd64.iso \
    -boot d \
    -drive file=gonas-test-disk-amd64.img,format=qcow2,if=virtio \
    -netdev user,id=net0 -device virtio-net-pci,netdev=net0 \
    -device virtio-gpu-pci -display default,show-cursor=on
```

注意**沒有** `-accel hvf`(架構不同,加不了速,加了也沒用)。慢,但能
驗證邏輯。

## 步驟 3:燒到真正的 USB 隨身碟

在 Mac mini 上把 amd64 那顆 ISO 燒錄到 USB(DVD-1 底的成品 ISO 有
~3.7GB+,準備一支**至少 8GB**、內容可以清空的隨身碟;之前 netinst 時代
一支 4GB 就夠,現在不夠了):

```
diskutil list
```

找到你插入的隨身碟對應的 `/dev/diskN`(**務必看清楚是哪一顆**——型號、
容量大小,不要跟 Mac mini 自己的硬碟搞混,選錯會直接砸掉你電腦本身的
資料)。確認後:

```
diskutil unmountDisk /dev/diskN
sudo dd if=dist/release/gonas-<version>-amd64.iso of=/dev/rdiskN bs=4m
```

注意 `of=` 用的是 `/dev/rdiskN`(前面加 `r` 的 raw disk 路徑,寫入速度
快很多),不是 `/dev/diskN`。寫完會看到 `dd` 印出寫入的區塊數跟耗時,
沒有其他訊息就是正常結束。也可以用 balenaEtcher(有 macOS 版圖形介面,
選 ISO、選目標隨身碟,介面上會清楚標示型號跟容量,比命令列不容易選錯),
兩種方式擇一即可。

## 步驟 4:準備真正的 x86 機器

插到你要測試/正式使用的那台 x86 機器之前:

- **先拔掉/斷開那台機器上任何已經有資料的磁碟,只留下要裝系統的那顆
  全新硬碟**——分割步驟如果選錯磁碟,寫入是不可逆的,尤其如果那台機器
  上還接著別的資料陣列,務必先物理拔線。
- 確認開機順序(BIOS/UEFI 設定)會優先從 USB 開機,或開機時按對應的
  熱鍵(常見是 F12/F11/Esc,依主機板廠牌而定)手動選一次開機裝置。
- 如果這台機器同時支援 Legacy BIOS 跟 UEFI 開機模式,兩種都能用
  (amd64 的 ISO 兩種模式都支援),但建議選 UEFI,跟大多數新硬體的
  預設行為一致。
- 如果這台機器**沒有一般的 VGA/HDMI 螢幕輸出**(伺服器主機板常見,只有
  序列埠主控台或 IPMI/BMC 遠端管理介面)——tty1 那個 GoNAS 品牌畫面
  (ASCII art + 網址)完全看不到,機器還是會正常開機、gonasd 一樣會
  啟動,只是找不到品牌畫面上顯示的網址,改到你的路由器/DHCP 伺服器的
  用戶端清單裡找這台機器的 IP。
- 真實硬體上如果跳出「缺少韌體,請插入另一份媒體」的畫面——這是某些
  網卡/儲存控制器需要專屬韌體檔案才能被完整偵測的已知限制(VM 用
  virtio 裝置不會遇到,是真機才會踩到的差異)。官方 DVD-1 通常比
  netinst 內附更多 non-free 韌體,踩到的機率較低,但不保證涵蓋所有
  硬體;preseed 對這一題一律回答「不要等額外媒體」讓安裝繼續走完,
  對應硬體事後若不能用,把型號記下來告訴我。

## 步驟 5:開機安裝,全程不需要按鍵

跟你在 Parallels 上看到的行為一樣,只會在下面兩種情況停下來要求手動
確認(刻意保留、不是 bug):

1. 如果這台機器插了不只一顆磁碟,會問要對哪一顆分割。
2. 選定磁碟後,最後會停在「確定要把這個分割配置寫入磁碟」的確認畫面
   ——**這裡選繼續/是**,才會真的往下走。

除了這兩種情況以外,如果安裝程式在其他地方停下來要求輸入(語系、
鍵盤、帳號之類),把畫面截圖或抄下來告訴我。

安裝完成後會自動重開機、自動退出安裝媒體(這時候可以直接拔掉 USB)。

## 步驟 6:開機後檢查清單(跟 arm64 那次完全一樣的項目)

1. 螢幕(或序列埠主控台)應該幾乎立刻顯示 GoNAS 的 ASCII art 狀態畫面,
   看到版本號、主機名稱、`http://<ip>:8291` 的網址。
2. 瀏覽器打開那個網址,應該看到**登入畫面**(不是首次建立帳號),用
   `gonas` / `gonas` 登入,應該**立刻被強制要求改密碼**才能進主畫面。
   **正式使用前務必先在信任的網路環境下完成這一步**,改完密碼、開了
   HTTPS 再接回一般網路——見 `build/appliance/README.md`「安全性
   提醒」一節。
3. 用鍵盤直接在這台機器上登入(或切到文字模式主控台),帳號密碼一樣是
   `gonas` / `gonas`,應該同樣被要求立刻設一組新密碼才給 shell。
4. 設完新密碼進到 shell 後依序確認:
   - `systemctl status gonas` → `active (running)`
   - `systemctl is-enabled gonas` → `enabled`
   - `hostnamectl` → 主機名稱顯示 `gonas`
   - `systemctl is-enabled ssh` → `enabled`,接著從 Mac mini
     `ssh gonas@<這台機器的IP>` 應該連得進來
   - `sudo -v` 或 `sudo id` → gonas 能用 sudo
   - `ls -l /usr/local/share/gonas/uninstall.sh` → 存在且有執行權限
5. 重開機一次,確認 tty1 品牌畫面下次開機一樣自動出現。

全部通過,這台機器就是一台可以正式使用的 GoNAS(x86)。

## 遇到問題怎麼辦

跟之前處理 arm64 的方式一樣,把這些帶回來:

- 卡在哪一步、完整錯誤訊息(截圖或文字都可以)。
- `journalctl -b -1` 或 `/etc/motd` 裡有沒有 `[gonas-late-command]`
  開頭的 log,尤其留意有沒有 `Permission denied`(這類問題在 arm64
  上已經抓過幾個根本原因,x86 理論上共用同一套修法,但畢竟是第一次
  在真機、而不是虛擬機上跑,仍然可能踩到新的、跟硬體相關的問題,例如
  網卡/儲存驅動缺韌體之類 VM 環境不會出現的狀況)。
