// Package config 負責讀取 GoNAS 的執行期設定。
//
// Phase 0 先用環境變數,之後 Phase 4(Web 介面)/ Phase 6(安全性)會
// 加上設定檔(/etc/gonas/config.yaml)與 Web UI 首次啟動精靈寫回設定檔。
package config

import (
	"os"
	"strconv"
)

// Config 是 GoNAS daemon 的執行期設定。
type Config struct {
	// ListenAddr 是 HTTP API / Web UI 監聽位址,例如 ":8291"。
	ListenAddr string
	// DataDir 存放 GoNAS 自身狀態(之後包含 SQLite、設定檔快取等)。
	DataDir string
}

// Load 從環境變數讀取設定,並套用合理的預設值。
//
//	GONAS_LISTEN_ADDR  預設 ":8291"
//	GONAS_DATA_DIR     預設 "/var/lib/gonas"
func Load() Config {
	return Config{
		ListenAddr: getEnv("GONAS_LISTEN_ADDR", ":8291"),
		DataDir:    getEnv("GONAS_DATA_DIR", "/var/lib/gonas"),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// getEnvInt 目前保留給之後的埠號/逾時等數值型設定使用。
func getEnvInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
