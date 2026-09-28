MODULE      := github.com/bng147/gonas
BINARY      := gonasd
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X '$(MODULE)/internal/version.Version=$(VERSION)' \
	-X '$(MODULE)/internal/version.Commit=$(COMMIT)' \
	-X '$(MODULE)/internal/version.BuildDate=$(BUILD_DATE)'

## MANIFEST_PUBKEY:(選用)自我更新的 ed25519 公鑰(hex,64 字元)。設了它,
## 編出來的 gonasd 就會「強制驗證更新 manifest 的簽章」——沒有對應私鑰簽的
## manifest 一律拒絕(fail-closed),見 internal/selfupdate 的 ManifestPublicKeyHex。
## 用 cmd/gonas-sign keygen 產生金鑰,再:
##   make build-amd64 MANIFEST_PUBKEY=<公鑰hex>
## 不設就維持原本行為(只驗 SHA256 + 強制 HTTPS,不驗簽,向後相容)。
MANIFEST_PUBKEY ?=
ifneq ($(strip $(MANIFEST_PUBKEY)),)
LDFLAGS += -X '$(MODULE)/internal/selfupdate.ManifestPublicKeyHex=$(MANIFEST_PUBKEY)'
endif

DIST := dist
RELEASE_DIR := $(DIST)/release

.PHONY: build build-amd64 build-arm64 build-armv7 build-all release run clean clean-cache vet fmt iso iso-amd64 iso-arm64

## build: 編譯給目前這台機器用的 binary(開發用)
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/gonasd

## build-amd64: 交叉編譯 linux/amd64 靜態執行檔
build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/gonasd

## build-arm64: 交叉編譯 linux/arm64 靜態執行檔(樹莓派4/5、多數 SBC)
build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 ./cmd/gonasd

## build-armv7: 交叉編譯 32-bit ARMv7 靜態執行檔(樹莓派2/3、Zero 2、
## 以及多數還在跑 32-bit 系統的小型/老舊 SBC)。GOARM=7 對應有硬體
## 浮點(VFPv3)的 ARMv7 核心;這份 binary 不能跑在更舊的 ARMv6
## (樹莓派 1/Zero)上——那類板子其實太弱、不適合當 NAS,真的需要時
## 把 GOARM 改成 6 即可(產出的 binary 反而向下相容 ARMv6/v7,只是
## 浮點少一點最佳化)。目前程式完全沒用到 sync/atomic 的 64-bit 操作,
## 所以沒有 32-bit ARM 上 64-bit atomic 需要 8-byte 對齊的那個雷。
build-armv7:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-armv7 ./cmd/gonasd

## build-all: 一次產出 amd64 + arm64 + armv7 三份靜態執行檔
build-all: build-amd64 build-arm64 build-armv7
	@echo "built:" && ls -la $(DIST)

## release: 把 build-all 的產物打包成每個架構各一份的 tarball,裡面含
## 執行檔(統一改名回 gonasd,不帶架構後綴,對應 install.sh 的
## 「release tarball 佈局」)、install.sh、uninstall.sh、gonas.service。
## 使用者下載解壓後直接 `sudo ./install.sh` 就能裝,不用自己編譯。
release: build-all
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	for arch in amd64 arm64 armv7; do \
		pkgdir=$(RELEASE_DIR)/gonas-$(VERSION)-linux-$$arch; \
		mkdir -p $$pkgdir; \
		cp $(DIST)/$(BINARY)-linux-$$arch $$pkgdir/$(BINARY); \
		chmod 0755 $$pkgdir/$(BINARY); \
		cp build/install.sh $$pkgdir/install.sh; \
		cp build/uninstall.sh $$pkgdir/uninstall.sh; \
		cp build/systemd/gonas.service $$pkgdir/gonas.service; \
		chmod 0755 $$pkgdir/install.sh $$pkgdir/uninstall.sh; \
		tar -C $(RELEASE_DIR) -czf $(RELEASE_DIR)/gonas-$(VERSION)-linux-$$arch.tar.gz gonas-$(VERSION)-linux-$$arch; \
		rm -rf $$pkgdir; \
	done
	@echo "release tarballs:" && ls -la $(RELEASE_DIR)

## run: 開發模式直接跑起來(監聽 :8291)
run:
	GONAS_DATA_DIR=./devdata go run ./cmd/gonasd

vet:
	go vet ./...

fmt:
	gofmt -l -s .

## iso-amd64: 產生 x86_64 的 GoNAS 開機即用安裝映像檔(需要真正的網路
## 連線去抓官方 Debian netinst ISO，還有 xorriso/wget——這個開發沙盒
## 沒有這些條件，這個 target 設計成在使用者自己的機器或 CI 上執行，
## 見 build/appliance/README.md)。
##
## 用 `sh build/appliance/build-iso.sh ...` 呼叫，不是直接
## `build/appliance/build-iso.sh ...` 靠檔案本身的可執行權限位元——
## 第十七輪覆閱實測抓到的問題：這份原始碼透過 zip 下載、在 macOS 上
## 用 Finder 解壓縮之後，`build-iso.sh` 的可執行權限位元掉了，
## `make iso-arm64` 直接以 `Permission denied` 失敗，即使檔案內容
## 完全正確也一樣。這跟第十四輪修 late-command.sh/install.sh 的
## exec-bit 問題是同一個類別，只是這次是自己的建置工具鏈本身踩到，
## 不是 ISO 裡的檔案——用 `sh <path>` 呼叫完全不依賴那個位元，
## 不管 tar/zip/git 在傳輸過程中有沒有保留它都能正常執行。
iso-amd64: release
	sh build/appliance/build-iso.sh amd64 $(VERSION)

## iso-arm64: 同上，產生 aarch64(樹莓派4/5、多數 SBC)版本的映像檔。
iso-arm64: release
	sh build/appliance/build-iso.sh arm64 $(VERSION)

## iso: 兩個架構的映像檔都做一次。
iso: iso-amd64 iso-arm64

## clean: 清掉編譯產物跟 release/ISO 輸出,但刻意保留
## dist/.cache/(build-iso.sh 快取下載回來的官方 Debian ISO 用的目錄,
## 見 build/appliance/build-iso.sh)——那份快取存在的唯一理由就是
## 「反覆重新建置 ISO 不用每次都重新下載幾百 MB」,如果 `make clean`
## 把它一起清掉,只要養成「clean 完再重新 build」的習慣,就等於快取
## 從來沒有真的發揮過作用。真的想清掉下載快取(例如懷疑快取的 ISO
## 損毀、想強制重抓最新的官方映像),用下面的 `clean-cache`。
clean:
	@if [ -d $(DIST) ]; then \
		find $(DIST) -mindepth 1 -maxdepth 1 ! -name .cache -exec rm -rf {} +; \
	fi
	rm -rf devdata

## clean-cache: 清掉 dist/.cache/ 下載快取(不影響 dist/ 底下其他東西)。
clean-cache:
	rm -rf $(DIST)/.cache
