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

.PHONY: build build-amd64 build-arm64 build-all run clean vet fmt

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

## run: 開發模式直接跑起來(監聽 :8291)
run:
	GONAS_DATA_DIR=./devdata go run ./cmd/gonasd

vet:
	go vet ./...

fmt:
	gofmt -l -s .

clean:
	rm -rf $(DIST) devdata
