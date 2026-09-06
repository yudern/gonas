MODULE      := github.com/bng147/gonas
BINARY      := gonasd
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X '$(MODULE)/internal/version.Version=$(VERSION)' \
	-X '$(MODULE)/internal/version.Commit=$(COMMIT)' \
	-X '$(MODULE)/internal/version.BuildDate=$(BUILD_DATE)'

DIST := dist
RELEASE_DIR := $(DIST)/release

.PHONY: build build-amd64 build-arm64 build-all release run clean clean-cache vet fmt iso iso-amd64 iso-arm64

## build: 編譯給目前這台機器用的 binary(開發用)
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/gonasd

## build-amd64: 交叉編譯 linux/amd64 靜態執行檔
build-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-amd64 ./cmd/gonasd

## build-arm64: 交叉編譯 linux/arm64 靜態執行檔(樹莓派4/5、多數 SBC)
build-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY)-linux-arm64 ./cmd/gonasd

## build-all: 一次產出 amd64 + arm64 兩份靜態執行檔
build-all: build-amd64 build-arm64
	@echo "built:" && ls -la $(DIST)

## release: 把 build-all 的產物打包成每個架構各一份的 tarball,裡面含
## 執行檔(統一改名回 gonasd,不帶架構後綴,對應 install.sh 的
## 「release tarball 佈局」)、install.sh、uninstall.sh、gonas.service。
## 使用者下載解壓後直接 `sudo ./install.sh` 就能裝,不用自己編譯。
release: build-all
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	for arch in amd64 arm64; do \
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
iso-amd64: release
	build/appliance/build-iso.sh amd64 $(VERSION)

## iso-arm64: 同上，產生 aarch64(樹莓派4/5、多數 SBC)版本的映像檔。
iso-arm64: release
	build/appliance/build-iso.sh arm64 $(VERSION)

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
