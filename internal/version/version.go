// Package version 提供編譯時注入的版本資訊。
package version

// 以下變數由 Makefile 透過 -ldflags "-X ..." 在編譯時注入。
// 開發模式下直接 go run/go build 不帶 ldflags 時,會落回這裡的預設值。
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info 是給 API 回應與 CLI 輸出用的版本資訊結構。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoOS      string `json:"goos"`
	GoArch    string `json:"goarch"`
}
