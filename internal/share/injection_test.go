package share

import "testing"

// TestSambaShare_RejectsConfigInjection 固化第三十三輪的修法:含換行/控制
// 字元/逗號的欄位在產生 smb.conf 之前就要被 Validate 擋下來,合法值照過。
func TestSambaShare_RejectsConfigInjection(t *testing.T) {
	bad := []Share{
		{Name: "ok", Path: "/srv/data\n\tguest ok = yes"},             // path 換行注入
		{Name: "ok", Path: "/srv/data", Comment: "hi\n[evil]"},        // comment 換行注入
		{Name: "a\nb", Path: "/srv/data"},                             // name 換行
		{Name: "ok", Path: "/srv/data", ValidUsers: []string{"a,b"}},  // valid user 逗號注入
		{Name: "ok", Path: "/srv/data", ValidUsers: []string{"x\ny"}}, // valid user 換行
	}
	for i, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("case %d: expected injection to be rejected, but Validate passed: %+v", i, s)
		}
	}

	// 合法值(含 samba 允許的路徑空白、正常 comment/valid users)仍要通過。
	good := Share{
		Name: "media", Path: "/srv/my data", Comment: "家庭媒體",
		ValidUsers: []string{"alice", "bob"},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("expected a legitimate share to pass, got: %v", err)
	}
}

// TestNFSExport_RejectsConfigInjection 同理驗證 NFS export 欄位。
func TestNFSExport_RejectsConfigInjection(t *testing.T) {
	bad := []Export{
		{Path: "/srv/data\n/etc /(rw)", Clients: []NFSClientRule{{CIDR: "192.168.1.0/24", Options: []string{"rw"}}}}, // path 換行注入
		{Path: "/srv/my data", Clients: []NFSClientRule{{CIDR: "192.168.1.0/24", Options: []string{"rw"}}}},          // path 空白破壞格式
		{Path: "/srv/data", Clients: []NFSClientRule{{CIDR: "1.2.3.0/24 *", Options: []string{"rw"}}}},               // cidr 空白
		{Path: "/srv/data", Clients: []NFSClientRule{{CIDR: "1.2.3.0/24", Options: []string{"rw,no_root_squash"}}}},  // option 逗號注入
		{Path: "/srv/data", Clients: []NFSClientRule{{CIDR: "1.2.3.0/24", Options: []string{"rw)\n/etc *(rw"}}}},     // option 換行+括號
	}
	for i, e := range bad {
		if err := e.Validate(); err == nil {
			t.Errorf("case %d: expected injection to be rejected, but Validate passed: %+v", i, e)
		}
	}

	good := Export{Path: "/srv/data", Clients: []NFSClientRule{
		{CIDR: "192.168.1.0/24", Options: []string{"rw", "sync", "no_subtree_check"}},
	}}
	if err := good.Validate(); err != nil {
		t.Errorf("expected a legitimate export to pass, got: %v", err)
	}
}
