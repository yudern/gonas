package api

import (
	"io/fs"
	"testing"
)

// TestVendorAssetsEmbedded 確保 xterm 前端資源真的被 go:embed 打包進二進位
// (放錯地方/忘了 commit 會讓互動式終端機悄悄退回內建模擬器)。這三個檔案
// 存在且有合理大小,就代表這個 build 內建了 xterm 渲染。
func TestVendorAssetsEmbedded(t *testing.T) {
	cases := map[string]int64{
		"webui/static/vendor/xterm.min.js":           100000, // xterm UMD ~280KB
		"webui/static/vendor/xterm.min.css":          1000,
		"webui/static/vendor/xterm-addon-fit.min.js": 500,
	}
	for path, minSize := range cases {
		info, err := fs.Stat(webUIFS, path)
		if err != nil {
			t.Errorf("vendored asset %s not embedded: %v", path, err)
			continue
		}
		if info.Size() < minSize {
			t.Errorf("%s is suspiciously small (%d bytes, want >= %d)", path, info.Size(), minSize)
		}
	}
}
