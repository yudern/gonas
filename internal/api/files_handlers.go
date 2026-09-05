package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/filemanager"
)

// writeFileManagerError 把 internal/filemanager 的 sentinel 錯誤對應到
// 合適的 HTTP 狀態碼，集中在一個地方維護，避免每支 handler 各自重複一份
// (而且容易漏掉某個錯誤類型)判斷邏輯。
func writeFileManagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoPoolConfigured), errors.Is(err, errArrayNotStarted):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, filemanager.ErrNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, filemanager.ErrAlreadyExists):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, filemanager.ErrPathEscapesRoot),
		errors.Is(err, filemanager.ErrNotADirectory),
		errors.Is(err, filemanager.ErrIsADirectory),
		errors.Is(err, filemanager.ErrInvalidName),
		errors.Is(err, filemanager.ErrNotValidUTF8Text),
		errors.Is(err, errMissingPath),
		errors.Is(err, errMissingFromOrTo),
		errors.Is(err, errNoUploadedFile):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, filemanager.ErrFileTooLargeForTextEdit):
		writeError(w, http.StatusRequestEntityTooLarge, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

type fileManagerStatusResponse struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// handleFilesStatus 讓 Web UI 在畫出檔案管理員頁面之前，先問一句「現在
// 能不能用」，藉此顯示清楚的提示(去設定陣列/去啟動陣列)而不是讓使用者
// 對著一頁 400/409 的錯誤訊息不知所措——這跟 handleDockerPing 對
// 應用程式頁面的角色是一樣的。
func (s *Server) handleFilesStatus(w http.ResponseWriter, r *http.Request) {
	if _, err := s.fileManagerRoot(); err != nil {
		writeJSON(w, http.StatusOK, fileManagerStatusResponse{Available: false, Reason: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, fileManagerStatusResponse{Available: true})
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	entries, err := filemanager.List(root, r.URL.Query().Get("path"))
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

type mkdirRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleFilesMkdir(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	var req mkdirRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	if err := filemanager.Mkdir(root, req.Path); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type moveOrCopyRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (s *Server) handleFilesMove(w http.ResponseWriter, r *http.Request) {
	s.handleFilesMoveOrCopy(w, r, filemanager.Move)
}

func (s *Server) handleFilesCopy(w http.ResponseWriter, r *http.Request) {
	s.handleFilesMoveOrCopy(w, r, filemanager.Copy)
}

func (s *Server) handleFilesMoveOrCopy(w http.ResponseWriter, r *http.Request, op func(root, from, to string) error) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	var req moveOrCopyRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		writeFileManagerError(w, errMissingFromOrTo)
		return
	}
	if err := op(root, req.From, req.To); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFilesDelete 預設是軟刪除(進回收桶)，只有明確帶
// ?permanent=true 才會真的立即刪除——「刪除」是檔案管理員裡最容易誤觸
// 的操作，預設值應該是比較安全、可以反悔的那一種，見
// internal/filemanager/trash.go 的套件說明。
func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	permanent := r.URL.Query().Get("permanent") == "true"
	if err := filemanager.Delete(root, relPath, permanent); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFilesDownload 直接把檔案內容串流回去，用 http.ServeContent 而不是
// 自己手動 io.Copy——ServeContent 免費附贈 Range 請求支援(瀏覽器/影片
// 播放器要求「從第 N 個 byte 開始」時可以正確回應 206 Partial Content),
// 這對大型檔案(影片、備份壓縮檔)的預覽/續傳體驗差很多,自己刻一份
// Range 處理邏輯既容易出錯又沒必要。
func (s *Server) handleFilesDownload(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	abs, err := filemanager.ResolvePath(root, relPath)
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			writeFileManagerError(w, filemanager.ErrNotFound)
			return
		}
		s.logger.Error("stat for download failed", "err", err, "path", relPath)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if info.IsDir() {
		writeFileManagerError(w, filemanager.ErrIsADirectory)
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		s.logger.Error("opening file for download failed", "err", err, "path", relPath)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer f.Close()

	filename := filepath.Base(abs)
	w.Header().Set("Content-Disposition", contentDisposition(filename))
	if ct := mime.TypeByExtension(filepath.Ext(filename)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, filename, info.ModTime(), f)
}

// handleFilesDownloadZip 把一個資料夾即時壓縮成 zip 串流回去(見
// internal/filemanager.WriteZip 的說明:邊走訪邊寫進回應本身，不會在
// 伺服器端先組出完整檔案)。大型資料夾可能要花不少時間,這支端點需要
// 豁免 http.Server 的 ReadTimeout/預設寫入行為，作法見
// handleFilesUpload 的說明(下載走的是回應寫入路徑，本來就不受
// ReadTimeout 影響,不需要額外處理;真正需要豁免的是上傳)。
func (s *Server) handleFilesDownloadZip(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	abs, err := filemanager.ResolvePath(root, relPath)
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	info, statErr := os.Stat(abs)
	if statErr != nil || !info.IsDir() {
		writeFileManagerError(w, filemanager.ErrNotADirectory)
		return
	}

	zipName := filepath.Base(abs) + ".zip"
	w.Header().Set("Content-Disposition", contentDisposition(zipName))
	w.Header().Set("Content-Type", "application/zip")
	if err := filemanager.WriteZip(w, root, relPath); err != nil {
		// 這個時間點 HTTP 狀態碼/部分內容可能已經送出去了，記錄下來就好，
		// 沒辦法再回一個乾淨的錯誤回應給用戶端。
		s.logger.Error("streaming zip download failed", "err", err, "path", relPath)
	}
}

func (s *Server) handleFilesSearch(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	result, err := filemanager.Search(root, r.URL.Query().Get("path"), r.URL.Query().Get("q"))
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type textFileResponse struct {
	Content string `json:"content"`
}

func (s *Server) handleFilesReadText(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	content, err := filemanager.ReadTextFile(root, relPath)
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, textFileResponse{Content: content})
}

func (s *Server) handleFilesWriteText(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	relPath := r.URL.Query().Get("path")
	if relPath == "" {
		writeFileManagerError(w, errMissingPath)
		return
	}
	var req textFileResponse
	if !readJSON(w, r, &req) {
		return
	}
	if err := filemanager.WriteTextFile(root, relPath, req.Content); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleFilesUpload 接收 multipart/form-data 上傳，支援單一請求裡放
// 多個檔案(瀏覽器的 <input type="file" multiple> 或拖放多個檔案時，
// 會在同一個表單裡送出好幾個同名的檔案欄位)。
//
// 這裡刻意呼叫 http.NewResponseController(w).SetReadDeadline 把這個請求
// 的讀取逾時整個關掉：cmd/gonasd/main.go 的 http.Server.ReadTimeout
// 是假設「所有請求都是小型 JSON」設的 20 秒(見那裡的註解)，對檔案
// 上傳這種「body 大小取決於使用者要傳多大的檔案、傳輸時間取決於網路
// 速度」的請求完全不適用——20 秒對區網傳一顆幾 GB 的影片檔來說毫無意義,
// 一定會被腰斬。Go 1.20 加入的 ResponseController 讓單一 handler 可以
// 覆蓋 server 全域的逾時設定,不需要為了這一支端點放寬全站的 slowloris
// 防護。
func (s *Server) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	dirPath := r.URL.Query().Get("path")

	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetReadDeadline(time.Time{})
		_ = rc.SetWriteDeadline(time.Time{})
	}

	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parsing upload request: %w", err))
		return
	}

	var saved []filemanager.Entry
	for {
		part, err := reader.NextPart()
		if err != nil {
			break // io.EOF = 沒有更多檔案了,其他錯誤也沒有更好的處理方式,直接結束迴圈
		}
		filename := part.FileName()
		if filename == "" {
			// 不是檔案欄位(例如表單裡混了其他一般欄位),略過。
			part.Close()
			continue
		}
		entry, err := filemanager.SaveStream(root, dirPath, filepath.Base(filename), part)
		part.Close()
		if err != nil {
			writeFileManagerError(w, err)
			return
		}
		saved = append(saved, entry)
	}

	if len(saved) == 0 {
		writeFileManagerError(w, errNoUploadedFile)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleFilesTrashList(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	entries, err := filemanager.ListTrash(root)
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleFilesTrashRestore(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	if err := filemanager.RestoreFromTrash(root, r.PathValue("id")); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFilesTrashDeleteItem(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	if err := filemanager.DeleteTrashItem(root, r.PathValue("id")); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleFilesTrashEmpty(w http.ResponseWriter, r *http.Request) {
	root, err := s.fileManagerRoot()
	if err != nil {
		writeFileManagerError(w, err)
		return
	}
	if err := filemanager.EmptyTrash(root); err != nil {
		writeFileManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// contentDisposition 組出一個 Content-Disposition: attachment 標頭值。
// 同時給 filename="..."(給不支援 RFC 5987 的舊瀏覽器，反斜線/雙引號
// 依 quoted-string 規則跳脫)跟 filename*=UTF-8”...(給看得懂的瀏覽器，
// 正確處理非 ASCII 檔名,例如中文檔名)兩種形式，前面的引號版本對
// 非 ASCII 字元只能盡力而為，看得懂 filename* 的瀏覽器會優先採用那個。
func contentDisposition(filename string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(filename)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, escaped, url.PathEscape(filename))
}
