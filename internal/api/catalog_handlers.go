package api

import (
	"context"
	"net/http"
	"time"

	"github.com/bng147/gonas/internal/appstore"
	"github.com/bng147/gonas/internal/state"
)

// catalogEntry 是 GET /appstore/catalog 回傳的單一範本,在 AppTemplate 之外多帶
// 一個 Source 欄位讓前端標示來源(內建 vs 遠端目錄)。用內嵌(embedding)讓
// AppTemplate 的欄位在 JSON 裡原樣攤平,前端既有的 renderCatalogEntry 讀 id/
// name/description/services 完全不用改,只是多了一個可選的 source badge。
type catalogEntry struct {
	appstore.AppTemplate
	Source string `json:"source"` // "builtin" 或 "remote"
}

// mergedCatalog 把內建目錄與遠端目錄快取合併成一份給前端顯示。內建優先:遠端
// 目錄裡任何跟內建「同 ID」的範本都被丟掉,這樣一個(可能被入侵或 MITM 的)
// 遠端目錄無法用一個惡意範本去「shadow」掉使用者信任的內建 Portainer/Jellyfin
// 等 —— 安裝時 resolveInstallTemplate/templateByID 也遵守同樣的「內建優先」。
func (s *Server) mergedCatalog() []catalogEntry {
	out := make([]catalogEntry, 0, len(builtinCatalog))
	builtinIDs := make(map[string]bool, len(builtinCatalog))
	for _, t := range builtinCatalog {
		builtinIDs[t.ID] = true
		out = append(out, catalogEntry{AppTemplate: t, Source: "builtin"})
	}

	s.catalogMu.RLock()
	remote := s.remoteCatalog
	s.catalogMu.RUnlock()
	for _, t := range remote {
		if builtinIDs[t.ID] {
			continue // 內建優先,不讓遠端 shadow
		}
		out = append(out, catalogEntry{AppTemplate: t, Source: "remote"})
	}
	return out
}

// templateByID 依 ID 找一個可安裝的範本,先內建、後遠端快取(內建優先)。
func (s *Server) templateByID(id string) (appstore.AppTemplate, bool) {
	for i := range builtinCatalog {
		if builtinCatalog[i].ID == id {
			return builtinCatalog[i], true
		}
	}
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	for i := range s.remoteCatalog {
		if s.remoteCatalog[i].ID == id {
			return s.remoteCatalog[i], true
		}
	}
	return appstore.AppTemplate{}, false
}

// refreshRemoteCatalog 依 state 裡的 AppCatalogURL 重新抓一次遠端目錄,把結果
// (或錯誤)寫進記憶體快取。網址是空字串時把快取清空並視為「沒有錯誤」。
// best-effort:抓取失敗只記在 catalogFetchErr,不回傳錯誤、不影響既有快取之外
// 的任何東西(呼叫端 —— 啟動時的 goroutine 與 HTTP handler —— 都不需要處理錯誤)。
func (s *Server) refreshRemoteCatalog(ctx context.Context) {
	url := s.store.Snapshot().AppCatalogURL
	if url == "" {
		s.catalogMu.Lock()
		s.remoteCatalog = nil
		s.catalogFetchedAt = time.Time{}
		s.catalogFetchErr = ""
		s.catalogSkipped = nil
		s.catalogMu.Unlock()
		return
	}

	templates, skipped, err := appstore.FetchCatalog(ctx, s.catalogHTTPClient, url)
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	s.catalogFetchedAt = time.Now()
	if err != nil {
		// 抓取失敗時保留上一次成功抓到的快取(如果有),只記錯誤 —— 遠端暫時
		// 掛掉不該讓使用者連之前抓到的目錄都看不到。
		s.catalogFetchErr = err.Error()
		s.logger.Warn("refreshing remote app catalog failed", "url", url, "err", err)
		return
	}
	s.remoteCatalog = templates
	s.catalogSkipped = skipped
	s.catalogFetchErr = ""
	s.logger.Info("remote app catalog refreshed", "url", url, "apps", len(templates), "skipped", len(skipped))
}

func (s *Server) handleAppstoreCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.mergedCatalog())
}

// catalogSourceResponse 是「遠端目錄設定」的狀態,給前端顯示目前網址、上次抓
// 取時間/結果、抓到幾個範本、被跳過幾個。
type catalogSourceResponse struct {
	URL         string     `json:"url"`
	FetchedAt   *time.Time `json:"fetchedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	RemoteCount int        `json:"remoteCount"`
	Skipped     []string   `json:"skipped,omitempty"`
}

func (s *Server) catalogSourceStatus() catalogSourceResponse {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	resp := catalogSourceResponse{
		URL:         s.store.Snapshot().AppCatalogURL,
		Error:       s.catalogFetchErr,
		RemoteCount: len(s.remoteCatalog),
		Skipped:     s.catalogSkipped,
	}
	if !s.catalogFetchedAt.IsZero() {
		t := s.catalogFetchedAt
		resp.FetchedAt = &t
	}
	return resp
}

func (s *Server) handleAppstoreCatalogSourceGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.catalogSourceStatus())
}

type catalogSourceSetRequest struct {
	URL string `json:"url"`
}

// handleAppstoreCatalogSourceSet 設定(或清空)遠端目錄網址,持久化後同步抓一
// 次,把抓取結果一起回給前端(讓使用者按「儲存」後馬上看到成功/失敗,不用再
// 自己按一次重新整理)。requireAdmin。清空網址(空字串)等於「只用內建目錄」。
func (s *Server) handleAppstoreCatalogSourceSet(w http.ResponseWriter, r *http.Request) {
	var req catalogSourceSetRequest
	if !readJSON(w, r, &req) {
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.AppCatalogURL = req.URL
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 同步抓一次(用請求的 context,逾時由 catalogHTTPClient.Timeout 控制),
	// 讓回應直接帶上結果。FetchCatalog 自己擋 scheme/大小/數量。
	s.refreshRemoteCatalog(r.Context())
	writeJSON(w, http.StatusOK, s.catalogSourceStatus())
}

// handleAppstoreCatalogRefresh 立刻重抓目前設定的遠端目錄,回傳最新狀態。
// requireAdmin。沒有設定網址時等於清空快取,回報 remoteCount=0。
func (s *Server) handleAppstoreCatalogRefresh(w http.ResponseWriter, r *http.Request) {
	s.refreshRemoteCatalog(r.Context())
	writeJSON(w, http.StatusOK, s.catalogSourceStatus())
}
