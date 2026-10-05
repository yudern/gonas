# 第三方前端資源(vendored)

這個資料夾放「必須離線可用、所以直接打包進二進位」的第三方前端資源。
目前需要的是互動式容器終端機用的 xterm.js:

- `xterm.min.js`           (xterm 5.3.0)
- `xterm.min.css`          (xterm 5.3.0)
- `xterm-addon-fit.min.js` (xterm-addon-fit 0.8.0)

這些檔案由 `//go:embed webui/static` 一併打包進 gonasd,執行期以 `/vendor/...`
提供給 Web UI。**不是必須的**:沒有這三個檔案時,互動式終端機會自動改用 GonasNAS
內建的輕量終端機模擬器(見 app.js 的 GonasTerminal,純原生 JS、零依賴),一樣能用
——放進這三個檔案只是把渲染升級成 xterm.js 的完整實作(更完整的 VT 相容性)。
放進去後重新 `make build`,前端會自動偵測並優先使用 xterm。

來源(擇一):
- cdnjs:https://cdnjs.cloudflare.com/ajax/libs/xterm/5.3.0/xterm.min.js 等
- npm:`npm pack xterm@5.3.0 xterm-addon-fit@0.8.0`,從 tarball 的
  `package/lib/xterm.js`(UMD)、`package/css/xterm.css`、
  `package/lib/xterm-addon-fit.js` 取出,分別更名為上面三個檔名。

取得後放進這個資料夾、重新 `make build` 即可。
