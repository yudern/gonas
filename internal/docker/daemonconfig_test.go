package docker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadDaemonConfig_MissingFileIsEmpty(t *testing.T) {
	view, err := ReadDaemonConfig(filepath.Join(t.TempDir(), "daemon.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(view.RegistryMirrors) != 0 || len(view.InsecureRegistries) != 0 {
		t.Errorf("expected empty view, got %+v", view)
	}
}

func TestWriteDaemonConfig_PreservesUnknownKeysAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	// 預先放一個帶「其他設定」的 daemon.json。
	orig := `{"storage-driver":"overlay2","log-opts":{"max-size":"10m"}}`
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	err := WriteDaemonConfig(path, DaemonConfigView{
		RegistryMirrors:    []string{"https://docker.m.daocloud.io"},
		InsecureRegistries: []string{"192.168.1.10:5000"},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	// 未知鍵保留。
	if _, ok := m["storage-driver"]; !ok {
		t.Error("storage-driver key was dropped")
	}
	if _, ok := m["log-opts"]; !ok {
		t.Error("log-opts key was dropped")
	}
	// 我們的兩個鍵寫進去了,且 ReadDaemonConfig 讀得回來。
	view, err := ReadDaemonConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RegistryMirrors) != 1 || view.RegistryMirrors[0] != "https://docker.m.daocloud.io" {
		t.Errorf("mirrors round-trip wrong: %+v", view.RegistryMirrors)
	}
	if len(view.InsecureRegistries) != 1 || view.InsecureRegistries[0] != "192.168.1.10:5000" {
		t.Errorf("insecure round-trip wrong: %+v", view.InsecureRegistries)
	}
}

func TestWriteDaemonConfig_EmptyListsRemoveKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := WriteDaemonConfig(path, DaemonConfigView{RegistryMirrors: []string{"https://m1"}}); err != nil {
		t.Fatal(err)
	}
	// 再寫入空清單應移除鍵(清空加速器回到預設)。
	if err := WriteDaemonConfig(path, DaemonConfigView{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	if _, ok := m[daemonJSONMirrorsKey]; ok {
		t.Errorf("empty mirrors should remove the key, got: %s", raw)
	}
}

func TestWriteDaemonConfig_RefusesCorruptExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteDaemonConfig(path, DaemonConfigView{RegistryMirrors: []string{"https://m"}}); err == nil {
		t.Error("expected refusal to overwrite a corrupt daemon.json")
	}
}

func TestValidateRegistryMirror(t *testing.T) {
	ok := []string{"https://docker.m.daocloud.io", "http://192.168.1.5:5000"}
	for _, s := range ok {
		if err := ValidateRegistryMirror(s); err != nil {
			t.Errorf("%q should be valid: %v", s, err)
		}
	}
	bad := []string{"", "docker.m.daocloud.io", "ftp://x", "https://a b"}
	for _, s := range bad {
		if err := ValidateRegistryMirror(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestValidateInsecureRegistry(t *testing.T) {
	if err := ValidateInsecureRegistry("192.168.1.10:5000"); err != nil {
		t.Errorf("host:port should be valid: %v", err)
	}
	for _, s := range []string{"", "http://x:5000", "a b:5000"} {
		if err := ValidateInsecureRegistry(s); err == nil {
			t.Errorf("%q should be rejected", s)
		}
	}
}
