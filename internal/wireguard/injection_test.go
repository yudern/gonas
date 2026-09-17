package wireguard

import "testing"

// TestConfig_RejectsInjection 固化第三十四輪:wg .conf 由 text/template 產生
// (不跳脫換行),含換行/控制字元/空白的欄位在產生前就要被 Validate 擋下,
// 合法設定照過。
func TestConfig_RejectsInjection(t *testing.T) {
	iface := InterfaceConfig{PrivateKey: "aGVsbG8=", Address: []string{"10.0.0.1/24"}, ListenPort: 51820}

	badPeers := []PeerConfig{
		{Name: "phone\nPublicKey = attacker", PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32"}},                       // name 換行注入
		{Name: "phone", PublicKey: "k\nEndpoint = evil:1", AllowedIPs: []string{"10.0.0.2/32"}},                          // key 換行
		{Name: "phone", PublicKey: "k with space", AllowedIPs: []string{"10.0.0.2/32"}},                                  // key 空白
		{Name: "phone", PublicKey: "k", Endpoint: "evil:1\nAllowedIPs = 0.0.0.0/0", AllowedIPs: []string{"10.0.0.2/32"}}, // endpoint 換行
		{Name: "phone", PublicKey: "k", AllowedIPs: []string{"10.0.0.2/32 0.0.0.0/0"}},                                   // allowedIP 空白
	}
	for i, p := range badPeers {
		c := Config{Interface: iface, Peers: []PeerConfig{p}}
		if err := c.Validate(); err == nil {
			t.Errorf("peer case %d: expected injection to be rejected, but Validate passed: %+v", i, p)
		}
	}

	// 介面欄位換行注入
	badIface := Config{Interface: InterfaceConfig{PrivateKey: "key\nListenPort = 1", Address: []string{"10.0.0.1/24"}, ListenPort: 51820}}
	if err := badIface.Validate(); err == nil {
		t.Error("expected interface private-key newline injection to be rejected")
	}

	// 合法設定要通過
	good := Config{Interface: iface, Peers: []PeerConfig{
		{Name: "Alice phone", PublicKey: "abcDEF123+/=", PresharedKey: "psk123+/=", Endpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.0.0.2/32", "10.0.0.3/32"}},
	}}
	if err := good.Validate(); err != nil {
		t.Errorf("expected a legitimate wireguard config to pass, got: %v", err)
	}
}
