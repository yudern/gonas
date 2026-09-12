package api

import (
	"sync"
	"testing"

	"github.com/bng147/gonas/internal/storage"
)

// TestArrayPointer_ConcurrentAccessNoRace 固化第三十二輪的並發修法:
// 設定池的 handler 會透過 setArray 換掉 array 指標,而背景監控輪詢會透過
// getArray 讀它。這個測試同時做這兩件事,靠 `go test -race` 抓有沒有
// data race——修法前(直接讀寫 s.array 無鎖)在 -race 下會紅;修法後
// (arrayMu 保護)乾淨通過。
func TestArrayPointer_ConcurrentAccessNoRace(t *testing.T) {
	s := &Server{}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.setArray(storage.NewArray(storage.PoolConfig{}))
		}()
		go func() {
			defer wg.Done()
			if a := s.getArray(); a != nil {
				_ = a.Status()
			}
		}()
	}
	wg.Wait()
}
