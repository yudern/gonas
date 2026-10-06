package api

import (
	"embed"
	"io/fs"
	"net/http"
)

// webUI 是內嵌的 Web 管理介面靜態檔案(HTML/CSS/JS，沒有前端建置工具,
// 見 webui/static/README 註解裡的說明)。用 embed 而不是在執行期從磁碟讀,
// 是為了維持「單一靜態執行檔、複製過去就能跑」的部署模型 —— 使用者的
// NAS 上不會有 npm、不會有這個 repo 的原始碼，只有 gonasd 這個執行檔本身。
//
//go:embed webui/static
var webUIFS embed.FS

// webUIHandler 回傳一個服務內嵌前端資源的 http.Handler。
func webUIHandler() http.Handler {
	sub, err := fs.Sub(webUIFS, "webui/static")
	if err != nil {
		// 這只可能是編譯期就該抓到的路徑打錯，panic 讓它在開發階段立刻炸出來,
		// 而不是包裝成執行期錯誤讓每個請求都悄悄失敗。
		panic("api: embedding webui/static: " + err.Error())
	}
	fsrv := http.FileServer(http.FS(sub))
	// 第六十四輪:內嵌檔案沒有修改時間(沒有 Last-Modified/ETag),瀏覽器可能
	// 沿用舊的 app.js/i18n.js——離線更新 gonasd 之後介面還在跑舊前端,修好的
	// 東西看起來「沒修」。明確要求每次重新驗證(檔案很小、只在區網內)。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		fsrv.ServeHTTP(w, r)
	})
}
