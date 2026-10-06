package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/doctor"
)

// offlineSourceList 是 late-command.sh 3.6 節寫的「本機離線 .deb 來源」清單檔。
// 存在就代表這台機器內建了一包可離線安裝的選用套件(見 build-iso.sh 4.8)。
// 用 var 而非 const,方便測試指到暫存檔驗證離線優先的行為。
var offlineSourceList = "/etc/apt/sources.list.d/gonas-offline.list"

// mirrorReachable 快速探測「這台機器能不能連到 Debian 鏡像」,用來決定離線
// 安裝失敗後要不要退回走網路(見 installOptionalPackage)。用 var 讓測試能覆寫,
// 避免測試真的去撥外網。3 秒逾時:通就是通,擋掉離線機器空等好幾分鐘的情況。
var mirrorReachable = func(ctx context.Context) bool {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", "deb.debian.org:443")
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// handleDoctorStatus 回傳每個選用外部套件「裝了沒」,給 Web「系統診斷」頁
// 顯示 + 判斷要不要在儀表板提示。唯讀,requireAuth 即可。
//
// 第六十輪(使用者實機):Docker 這一項要跟儀表板/應用頁「講同一個故事」。
// doctor.RunPackages() 用 exec.LookPath("docker") 找的是「docker 這個 CLI
// 執行檔在不在 gonasd 的 PATH 上」;但儀表板跟應用頁判斷 Docker 能不能用,
// 靠的是 s.docker.Ping()——直接連 /var/run/docker.sock 問 daemon 通不通,
// 根本不需要 CLI。兩者測的是不同東西,實機上就出現了使用者回報的矛盾:
// 「儀表板顯示 Docker 可用(daemon 在跑),診斷頁卻顯示未安裝(gonasd 的
// PATH 上找不到 docker CLI,或 LookPath 當下沒找到)」。對使用者來說「Docker
// 到底能不能用」才是重點,而 daemon 通得到就代表能用。所以這裡把 daemon ping
// 的結果疊上去:只要 daemon 通得到,docker 這項一律標成已安裝,讓診斷頁、
// 儀表板提示、應用頁三個地方對 Docker 的狀態完全一致。ping 只用來「補正成
// 已安裝」,不會把 LookPath 已判定安裝的反寫成未安裝(daemon 剛好沒啟動、
// 但 CLI 在,仍算裝了)。
func (s *Server) handleDoctorStatus(w http.ResponseWriter, r *http.Request) {
	pkgs := doctor.RunPackages()
	if s.docker != nil && s.docker.Ping(r.Context()) == nil {
		for i := range pkgs {
			if pkgs[i].Apt == "docker.io" {
				pkgs[i].Installed = true
				pkgs[i].Missing = nil
			}
		}
	}
	writeJSON(w, http.StatusOK, pkgs)
}

type doctorInstallRequest struct {
	Apt string `json:"apt"`
}

// errPackageNotInstallable 是固定英文錯誤(前端 errorMap 翻譯):送來的套件名
// 不在 doctor.OptionalPackages 白名單裡。這道白名單是「一鍵安裝」不會變成
// 任意 apt install 注入的關鍵。
var errPackageNotInstallable = errors.New("that package is not in the installable optional-dependency list")

// errInstallInProgress 是固定英文錯誤(前端 errorMap 翻譯):已經有一個
// 一鍵安裝在跑,同一時間只允許一個(見 Server.doctorInstalling)。
var errInstallInProgress = errors.New("a package install is already in progress, please wait for it to finish")

// errPackageUnavailable 是固定英文錯誤(前端 errorMap 翻譯):apt 在目前的
// 套件來源裡找不到這個套件(「Unable to locate package」/「no installation
// candidate」)。第五十三輪使用者實機遇到:這台 NAS 是離線安裝的,開機後
// 若沒有對外網路(或連不到 Debian 鏡像),apt 的套件索引是空的,一鍵補裝
// 就會這樣失敗。給一個看得懂、可行動的訊息,而不是把 apt 的原始英文錯誤
// 直接丟給使用者。
var errPackageUnavailable = errors.New("apt could not find that package in the current sources — this NAS may have no internet access or cannot reach the Debian mirror; installing optional packages needs a working network connection")

// handleDoctorInstall 用 apt 一鍵補裝一個選用套件(修掉「原廠映像沒裝
// mergerfs/snapraid/samba/docker,新手在嚮導建立儲存池那一步卡死、而文件叫
// 他去的 Doctor 頁面又不存在」這個第三十輪覆核抓到的最大產品阻斷)。
// requireAdmin(見 router.go)。
func (s *Server) handleDoctorInstall(w http.ResponseWriter, r *http.Request) {
	var req doctorInstallRequest
	if !readJSON(w, r, &req) {
		return
	}
	if !doctor.IsInstallable(req.Apt) {
		writeError(w, http.StatusBadRequest, errPackageNotInstallable)
		return
	}

	// single-flight(第五十二輪覆核 S-3):同一時間只允許一個安裝在跑。
	// CompareAndSwap 搶不到就代表已經有一個在裝,直接回 409,不要放兩個
	// apt-get 去撞 dpkg 的獨佔鎖(那會讓第二個以難懂的鎖錯誤失敗)。搶到的
	// 那個在函式結束時把旗標放回 false。
	if !s.doctorInstalling.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, errInstallInProgress)
		return
	}
	defer s.doctorInstalling.Store(false)

	// 刻意用「跟 HTTP 請求脫鉤」的 context:apt 安裝可能要跑好幾分鐘,若綁在
	// r.Context() 上,使用者一關瀏覽器/連線一斷就會把 apt 半路砍掉,可能讓
	// dpkg 卡在半安裝狀態(比沒裝更難處理)。給一個獨立、10 分鐘上限的
	// context 讓它自己跑完;前端則顯示「安裝中,可能需要幾分鐘」的等待狀態。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.logger.Info("installing optional package via web Doctor", "apt", req.Apt)
	out, err := s.installOptionalPackage(ctx, req.Apt)
	if err != nil {
		s.logger.Error("optional package install failed", "apt", req.Apt, "err", err, "out", string(out))
		// 第六十一輪:一律把 apt 的真實報錯(E:/相依性/權限那幾行)放在 detail
		// 裡回給前端顯示——之前所有「找不到套件」都被收斂成一句「可能沒有聯網」,
		// 把真正的原因(_apt 讀不到離線倉庫的 Permission denied)藏起來,使用者
		// 跟我們都看不到,問題才會一修再犯。
		detail := aptErrorDetail(out, err, 12)
		if errors.Is(err, errOfflineInstallFailed) {
			// 機器上有內建離線倉庫、但從它裝不起來:這不是「沒聯網」的問題,
			// 不能再顯示那句誤導的訊息。
			writeErrorDetail(w, http.StatusBadGateway, errOfflineInstallFailed, detail)
			return
		}
		combined := strings.ToLower(err.Error() + " " + string(out))
		if strings.Contains(combined, "unable to locate package") ||
			strings.Contains(combined, "has no installation candidate") ||
			strings.Contains(combined, "could not resolve") ||
			strings.Contains(combined, "failed to fetch") {
			// 沒有內建離線倉庫、走網路來源也找不到 → 才是真的「需要網路」。
			writeErrorDetail(w, http.StatusBadGateway, errPackageUnavailable, detail)
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, errors.New("apt-get install failed"), detail)
		return
	}
	// 第五十八輪產品覆核(#3):裝好服務後,把先前「設定已存、但當時服務還沒
	// 裝、所以套用失敗」的共享/匯出重新套用一次——否則使用者的自然流程
	// (先建共享→發現沒生效→來 Doctor 裝 samba/nfs→裝好)會卡在「state 裡有
	// 設定,但從沒推到剛裝好的服務」。best-effort:重套失敗只記 log,不影響
	// 「安裝成功」本身。
	// 已知限制:使用者帳號的 Samba 密碼無法在這裡重新同步(我們只存雜湊、不存
	// 明文密碼),裝好 samba 後既有使用者需各自重設一次密碼才能用 SMB 登入。
	reapplied := ""
	switch req.Apt {
	case "samba":
		if applied, warn := s.applySambaConfig(r, s.store.Snapshot().Shares); applied {
			reapplied = "samba shares re-applied"
		} else if warn != "" {
			s.logger.Warn("re-applying samba shares after install did not fully succeed", "warn", warn)
		}
	case "nfs-kernel-server":
		if applied, warn := s.applyExportsConfig(r, s.store.Snapshot().Exports); applied {
			reapplied = "nfs exports re-applied"
		} else if warn != "" {
			s.logger.Warn("re-applying nfs exports after install did not fully succeed", "warn", warn)
		}
	case "docker.io":
		// 第五十八輪產品覆核(#10):docker.io 裝好後 daemon 不一定會自動啟動/
		// 開機啟用,儀表板會一直顯示「Docker 無法使用」、同一個「去 Doctor」連結
		// 又剛按過,使用者卡住。best-effort 啟用+啟動 docker 服務,讓它裝完就能用。
		if out, err := s.runner.Run(ctx, "systemctl", "enable", "--now", "docker"); err != nil {
			s.logger.Warn("docker.io installed but enabling/starting the docker service failed", "err", err, "out", string(out))
		} else {
			reapplied = "docker service started"
		}
	}
	resp := map[string]string{"apt": req.Apt, "status": "installed"}
	if reapplied != "" {
		resp["reapplied"] = reapplied
	}
	writeJSON(w, http.StatusOK, resp)
}

// installOptionalPackage 安裝一個選用套件,策略是「離線優先、網路後援」:
//
// 第五十九輪(使用者實機 + 截圖定位的真正 bug):這台 NAS 是離線用的、沒有
// 對外網路。原本的流程一律先跑「會連網路來源」的 `apt-get update`,在離線機器
// 上會卡在「Connecting to deb.debian.org」很久,而且沒把本機離線來源乾淨地
// 索引起來,接著 `apt-get install` 就報「Unable to locate package」——明明本機
// 那包 .deb 全都在。這正是「離線安裝」不該發生的事。
//
// 修法:如果機器上有內建的本機離線來源(gonas-offline.list),就「只用這個
// 來源」做 update + install(用 apt 的 Dir::Etc::sourcelist / sourceparts 覆寫,
// 把網路來源整個排除在外)——file:// 來源不需要網路、瞬間完成,離線也絕不會
// 卡。只有在「沒有離線來源」或「離線包裡沒有這個套件」時,才退回走完整
// (含網路)來源,對應「機器剛好有網路、要裝離線包裡沒有的東西」的情況。
// 這才真正做到使用者要的「離線也能裝、有網路也能裝」。
func (s *Server) installOptionalPackage(ctx context.Context, apt string) ([]byte, error) {
	installArgsNoninteractive := func(extra ...string) []string {
		a := []string{"DEBIAN_FRONTEND=noninteractive", "apt-get"}
		a = append(a, extra...)
		a = append(a, "install", "-y", "--no-install-recommends", apt)
		return a
	}

	// 第六十一輪:每次安裝前先確保離線倉庫在 _apt 讀得到的位置、apt 來源指向
	// 它(舊版放在 0750 的 /var/lib/gonas/debs 底下 → apt 讀不到,見
	// offline_repo.go)。best-effort,失敗只記 log,後面的 apt 輸出會說明原因。
	if err := ensureOfflineRepo(s.logger); err != nil {
		s.logger.Warn("preparing the bundled offline apt repo failed", "err", err)
	}

	if _, statErr := os.Stat(offlineSourceList); statErr == nil {
		// 只用本機離線來源:Dir::Etc::sourcelist 指到離線清單、sourceparts 指到
		// /dev/null(等於「沒有其他來源檔」),apt 就完全看不到網路來源,不會連網。
		localOnly := []string{
			"-o", "Dir::Etc::sourcelist=" + offlineSourceList,
			"-o", "Dir::Etc::sourceparts=/dev/null",
		}
		// 只索引本機來源(file://,不連網、很快)。best-effort,但輸出保留下來,
		// 失敗時一起回給前端(真正的錯——例如 Permission denied——常常出在這一步)。
		updateArgs := append(append([]string{}, localOnly...), "update")
		updOut, updErr := s.runner.Run(ctx, "apt-get", updateArgs...)
		if updErr != nil {
			s.logger.Warn("local-only apt-get update failed (continuing to try install)", "apt", apt, "err", updErr, "out", string(updOut))
		}
		out, err := s.runner.Run(ctx, "env", installArgsNoninteractive(localOnly...)...)
		if err == nil {
			s.logger.Info("installed optional package from the local offline repo (no network used)", "apt", apt)
			return out, nil
		}
		combined := append(append([]byte{}, updOut...), '\n')
		if updErr != nil {
			combined = append(combined, []byte(updErr.Error()+"\n")...)
		}
		combined = append(combined, out...)
		combined = append(combined, []byte("\n"+err.Error())...)
		// 離線包裡沒有(或其他原因)→ 只有在「真的連得到鏡像」時才退回走網路。
		// 第六十輪:連不到鏡像就直接回錯,不要卡在網路 apt-get update 上。
		// 第六十一輪:回的是「離線倉庫安裝失敗」+ apt 真實輸出,而不是「可能
		// 沒有聯網」——這台機器明明有離線倉庫,那句話只會誤導。
		if !mirrorReachable(ctx) {
			s.logger.Info("offline-only install failed and the mirror is unreachable; not hanging on a network fallback", "apt", apt, "out", string(combined))
			return combined, errOfflineInstallFailed
		}
		s.logger.Info("offline-only install did not succeed; mirror looks reachable, falling back to network sources", "apt", apt, "out", string(combined))
	}

	// 後援:走完整來源(含網路)。給「沒有離線包、但機器有網路」的情況。
	if out, err := s.runner.Run(ctx, "apt-get", "update"); err != nil {
		s.logger.Warn("apt-get update reported an error before doctor install (continuing)", "apt", apt, "err", err, "out", string(out))
	}
	return s.runner.Run(ctx, "env", installArgsNoninteractive()...)
}
