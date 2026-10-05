package docker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultDaemonJSONPath 是 Docker daemon 設定檔的標準位置。抽成常數讓
// 呼叫端(internal/api)可以在測試時指到暫存檔,不去動真的系統檔。
const DefaultDaemonJSONPath = "/etc/docker/daemon.json"

// DaemonConfigView 是 GoNAS 會管理的那幾個 daemon.json 欄位的檢視。刻意只
// 涵蓋「鏡像加速 / 自架 registry」相關的兩個欄位,其餘欄位(storage-driver、
// log-opts、使用者手動加的任何設定)read-modify-write 時原樣保留、絕不覆寫。
//
// 為什麼只做這兩個:它們都是 dockerd 用 SIGHUP(systemctl reload docker)就能
// 熱套用的設定,不需要 restart dockerd、也就不會中斷正在跑的容器 —— 對一台
// NAS 來說「改個鏡像源要重啟 Docker、容器全斷」是不能接受的。
type DaemonConfigView struct {
	// RegistryMirrors 是 Docker Hub 的鏡像加速地址(https://... ),拉
	// docker.io 的映像時 dockerd 會優先走這些鏡像。中國網路最常用。
	RegistryMirrors []string `json:"registryMirrors"`
	// InsecureRegistries 是允許以 HTTP(或自簽憑證 HTTPS)存取的 registry
	// 位址(host[:port]),給自架的內網 registry 用。
	InsecureRegistries []string `json:"insecureRegistries"`
}

// daemonJSONMirrorsKey / daemonJSONInsecureKey 是 daemon.json 裡對應的鍵名
// (dockerd 定義的,連字號格式)。
const (
	daemonJSONMirrorsKey  = "registry-mirrors"
	daemonJSONInsecureKey = "insecure-registries"
)

// ReadDaemonConfig 讀出目前 daemon.json 裡 GoNAS 管理的那兩個欄位。檔案不存在
// 視為「兩者都空」而不是錯誤 —— 全新機器還沒有 daemon.json 是正常情況。檔案
// 存在但不是合法 JSON 物件才回錯(那是使用者手改壞了,應該讓他知道)。
func ReadDaemonConfig(path string) (DaemonConfigView, error) {
	var view DaemonConfigView
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return view, nil
		}
		return view, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return view, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return view, fmt.Errorf("%s is not a valid JSON object: %w", path, err)
	}
	if v, ok := m[daemonJSONMirrorsKey]; ok {
		_ = json.Unmarshal(v, &view.RegistryMirrors)
	}
	if v, ok := m[daemonJSONInsecureKey]; ok {
		_ = json.Unmarshal(v, &view.InsecureRegistries)
	}
	if view.RegistryMirrors == nil {
		view.RegistryMirrors = []string{}
	}
	if view.InsecureRegistries == nil {
		view.InsecureRegistries = []string{}
	}
	return view, nil
}

// WriteDaemonConfig 把 view 裡的兩個欄位寫回 daemon.json,**保留**檔案裡其他所有
// 鍵(read-modify-write)。空清單代表「移除這個鍵」,讓使用者可以清空加速器回到
// 預設行為,而不是留一個空陣列。原子寫入(暫存檔 + rename),不會留半份檔。
func WriteDaemonConfig(path string, view DaemonConfigView) error {
	m := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("refusing to overwrite %s: it is not a valid JSON object (%w)", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	setOrDelete := func(key string, vals []string) error {
		if len(vals) == 0 {
			delete(m, key)
			return nil
		}
		enc, err := json.Marshal(vals)
		if err != nil {
			return err
		}
		m[key] = enc
		return nil
	}
	if err := setOrDelete(daemonJSONMirrorsKey, view.RegistryMirrors); err != nil {
		return err
	}
	if err := setOrDelete(daemonJSONInsecureKey, view.InsecureRegistries); err != nil {
		return err
	}

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding daemon.json: %w", err)
	}
	out = append(out, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".daemon-json-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// ValidateRegistryMirror 檢查一條鏡像加速地址:必須是 http:// 或 https:// 開頭、
// 不含空白/控制字元(會進 daemon.json,格式必須乾淨)。
func ValidateRegistryMirror(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("registry mirror must not be empty")
	}
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return fmt.Errorf("registry mirror %q must start with http:// or https://", s)
	}
	if strings.ContainsAny(s, " \t\r\n\"'`") {
		return fmt.Errorf("registry mirror %q must not contain spaces or quotes", s)
	}
	return nil
}

// ValidateInsecureRegistry 檢查一條 insecure registry 位址:host[:port] 或
// CIDR,不含 scheme、空白、引號。
func ValidateInsecureRegistry(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("insecure registry must not be empty")
	}
	if strings.Contains(s, "://") {
		return fmt.Errorf("insecure registry %q must be host[:port], without http:// or https://", s)
	}
	if strings.ContainsAny(s, " \t\r\n\"'`") {
		return fmt.Errorf("insecure registry %q must not contain spaces or quotes", s)
	}
	return nil
}
