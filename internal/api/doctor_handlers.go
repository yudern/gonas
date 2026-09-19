package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/doctor"
)

// handleDoctorStatus 回傳每個選用外部套件「裝了沒」,給 Web「系統診斷」頁
// 顯示 + 判斷要不要在儀表板提示。唯讀,requireAuth 即可。
func (s *Server) handleDoctorStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, doctor.RunPackages())
}

type doctorInstallRequest struct {
	Apt string `json:"apt"`
}

// errPackageNotInstallable 是固定英文錯誤(前端 errorMap 翻譯):送來的套件名
// 不在 doctor.OptionalPackages 白名單裡。這道白名單是「一鍵安裝」不會變成
// 任意 apt install 注入的關鍵。
var errPackageNotInstallable = errors.New("that package is not in the installable optional-dependency list")

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

	// 刻意用「跟 HTTP 請求脫鉤」的 context:apt 安裝可能要跑好幾分鐘,若綁在
	// r.Context() 上,使用者一關瀏覽器/連線一斷就會把 apt 半路砍掉,可能讓
	// dpkg 卡在半安裝狀態(比沒裝更難處理)。給一個獨立、10 分鐘上限的
	// context 讓它自己跑完;前端則顯示「安裝中,可能需要幾分鐘」的等待狀態。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.logger.Info("installing optional package via web Doctor", "apt", req.Apt)
	// 先更新套件列表(best-effort:失敗就讓後面的 install 自己回報真正原因),
	// 再非互動安裝。用 `env DEBIAN_FRONTEND=noninteractive ...` 避免 debconf
	// 跳出互動式問題把安裝卡住;全程 argv 傳參、不經過 shell,套件名又已經過
	// 白名單,沒有指令注入風險。
	_, _ = s.runner.Run(ctx, "apt-get", "update")
	if _, err := s.runner.Run(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends", req.Apt); err != nil {
		s.logger.Error("optional package install failed", "apt", req.Apt, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"apt": req.Apt, "status": "installed"})
}
