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
