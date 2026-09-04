package api

import (
	"net/http"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/storage"
)

// handleStorageDisks 探測系統上目前有哪些區塊裝置。這是唯讀操作,
// 所以不需要陣列先被設定好才能呼叫 —— 使用者第一次設定 pool 之前,
// 就是靠這支 API 看到「有哪些硬碟可以選」。
func (s *Server) handleStorageDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := storage.DiscoverDisks(r.Context(), s.runner)
	if err != nil {
		s.logger.Error("disk discovery failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, disks)
}

// handleStorageArrayStatus 回傳目前陣列的狀態。還沒有人設定過 pool 時,
// 回傳 "unconfigured" 而不是錯誤 —— 這是合法的初始狀態,不是異常。
func (s *Server) handleStorageArrayStatus(w http.ResponseWriter, r *http.Request) {
	if s.array == nil {
		writeJSON(w, http.StatusOK, storage.Status{State: "unconfigured"})
		return
	}
	writeJSON(w, http.StatusOK, s.array.Status())
}

// handleStoragePoolSet 建立或取代目前的 pool 設定並持久化。刻意只驗證、
// 儲存設定,不會連帶啟動陣列 —— 「設定陣列該長什麼樣子」跟「掛載陣列讓它
// 可以讀寫」是兩個語意不同的動作,呼叫端要另外打 /array/start,理由跟
// storage.Array.Start 自己的註解一致(不要把多個危險動作隱含綁在一起)。
func (s *Server) handleStoragePoolSet(w http.ResponseWriter, r *http.Request) {
	var pool storage.PoolConfig
	if !readJSON(w, r, &pool) {
		return
	}
	if err := pool.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if err := s.store.Update(func(st *state.State) error {
		st.Pool = &pool
		return nil
	}); err != nil {
		s.logger.Error("persisting pool config failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	// 換掉記憶體裡的 Array 物件：新設定跟舊陣列的執行狀態沒有關係,
	// 一律視為一個全新的、還沒啟動的陣列，即使舊陣列當時是 started 也一樣
	// ——「編輯設定」不該悄悄延續舊的執行狀態。
	s.array = storage.NewArray(pool)

	writeJSON(w, http.StatusOK, s.array.Status())
}

func (s *Server) handleStorageArrayStart(w http.ResponseWriter, r *http.Request) {
	if s.array == nil {
		writeError(w, http.StatusConflict, errNoPoolConfigured)
		return
	}
	if err := s.array.Start(r.Context(), s.runner); err != nil {
		s.logger.Error("starting array failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.array.Status())
}

func (s *Server) handleStorageArrayStop(w http.ResponseWriter, r *http.Request) {
	if s.array == nil {
		writeError(w, http.StatusConflict, errNoPoolConfigured)
		return
	}
	if err := s.array.Stop(r.Context(), s.runner); err != nil {
		s.logger.Error("stopping array failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.array.Status())
}
