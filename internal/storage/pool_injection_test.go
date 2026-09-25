package storage

import "testing"

// TestPoolConfig_RejectsInjection 固化第三十四輪:snapraid.conf 由 text/template
// 產生(不跳脫換行)、line-based、以空白分隔,含換行/空白的名稱/路徑在產生前
// 就要被 Validate 擋下,合法設定照過。
func TestPoolConfig_RejectsInjection(t *testing.T) {
	base := func() PoolConfig {
		return PoolConfig{
			Name:         "tank",
			MountPoint:   "/mnt/tank",
			DataDisks:    []string{"/mnt/disk1", "/mnt/disk2"},
			ParityDisks:  []string{"/mnt/parity1"},
			ContentFiles: []string{"/mnt/disk1", "/mnt/disk2"},
		}
	}

	// 合法設定先確認會過
	if err := base().Validate(); err != nil {
		t.Fatalf("expected a legitimate pool to pass, got: %v", err)
	}

	bad := []func(PoolConfig) PoolConfig{
		func(c PoolConfig) PoolConfig { c.Name = "tank\ndata d9 /etc"; return c },                 // name 換行注入
		func(c PoolConfig) PoolConfig { c.Name = "my tank"; return c },                            // name 空白(會壞檔名)
		func(c PoolConfig) PoolConfig { c.MountPoint = "/mnt/t\nparity /evil"; return c },         // mountpoint 換行
		func(c PoolConfig) PoolConfig { c.DataDisks = []string{"/mnt/d1", "/mnt/d 2"}; return c }, // 路徑空白破壞 `data dN <path>`
		func(c PoolConfig) PoolConfig { c.ParityDisks = []string{"/mnt/p\t1"}; return c },         // 路徑 tab
	}
	for i, mut := range bad {
		if err := mut(base()).Validate(); err == nil {
			t.Errorf("case %d: expected injection/format-breaking value to be rejected, but Validate passed", i)
		}
	}
}

// TestPoolConfig_ConsistencyChecks 固化第五十六輪覆核(QA5):補上的一致性檢查
// —— 空的 content 檔位置、以及聯合掛載點剛好等於某顆資料/同位碟的掛載點。
func TestPoolConfig_ConsistencyChecks(t *testing.T) {
	base := func() PoolConfig {
		return PoolConfig{
			Name:         "tank",
			MountPoint:   "/mnt/tank",
			DataDisks:    []string{"/mnt/disk1", "/mnt/disk2"},
			ParityDisks:  []string{"/mnt/parity1"},
			ContentFiles: []string{"/mnt/disk1", "/mnt/disk2"},
		}
	}
	bad := map[string]func(PoolConfig) PoolConfig{
		"empty content file":        func(c PoolConfig) PoolConfig { c.ContentFiles = []string{"/mnt/disk1", ""}; return c },
		"mountpoint == data disk":   func(c PoolConfig) PoolConfig { c.MountPoint = "/mnt/disk1"; return c },
		"mountpoint == parity disk": func(c PoolConfig) PoolConfig { c.MountPoint = "/mnt/parity1"; return c },
	}
	for name, mut := range bad {
		if err := mut(base()).Validate(); err == nil {
			t.Errorf("%s: expected Validate to reject it, but it passed", name)
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("expected the legitimate base config to still pass, got: %v", err)
	}
}
