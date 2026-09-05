package wireguard

import (
	"strings"
	"testing"
)

func validInterface() InterfaceConfig {
	return InterfaceConfig{PrivateKey: "aGVsbG8gd29ybGQgcHJpdmF0ZWtleQ==", Address: []string{"10.10.0.1/24"}, ListenPort: 51820}
}

func validPeer() PeerConfig {
	return PeerConfig{Name: "phone", PublicKey: "cGVlcnB1YmxpY2tleQ==", AllowedIPs: []string{"10.10.0.2/32"}}
}

func TestInterfaceConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     InterfaceConfig
		wantErr bool
	}{
		{"valid", validInterface(), false},
		{"missing private key", InterfaceConfig{Address: []string{"10.10.0.1/24"}, ListenPort: 51820}, true},
		{"missing address", InterfaceConfig{PrivateKey: "x", ListenPort: 51820}, true},
		{"port zero", InterfaceConfig{PrivateKey: "x", Address: []string{"10.10.0.1/24"}, ListenPort: 0}, true},
		{"port negative", InterfaceConfig{PrivateKey: "x", Address: []string{"10.10.0.1/24"}, ListenPort: -1}, true},
		{"port too large", InterfaceConfig{PrivateKey: "x", Address: []string{"10.10.0.1/24"}, ListenPort: 70000}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPeerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		peer    PeerConfig
		wantErr bool
	}{
		{"valid", validPeer(), false},
		{"missing name", PeerConfig{PublicKey: "x", AllowedIPs: []string{"10.0.0.2/32"}}, true},
		{"missing public key", PeerConfig{Name: "phone", AllowedIPs: []string{"10.0.0.2/32"}}, true},
		{"missing allowed ips", PeerConfig{Name: "phone", PublicKey: "x"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.peer.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConfig_Validate_RejectsDuplicatePeerPublicKeys(t *testing.T) {
	peer := validPeer()
	cfg := Config{Interface: validInterface(), Peers: []PeerConfig{peer, peer}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate peer public keys to be rejected")
	}
}

func TestConfig_Validate_PropagatesInterfaceError(t *testing.T) {
	cfg := Config{Interface: InterfaceConfig{}, Peers: nil}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected an invalid interface config to fail validation")
	}
}

func TestGenerateConfig_RendersExpectedSections(t *testing.T) {
	cfg := Config{
		Interface: InterfaceConfig{
			PrivateKey: "SERVERPRIVATEKEY==",
			Address:    []string{"10.10.0.1/24", "fd00::1/64"},
			ListenPort: 51820,
		},
		Peers: []PeerConfig{
			{
				Name:                "laptop",
				PublicKey:           "LAPTOPPUBLICKEY==",
				PresharedKey:        "PRESHAREDKEY==",
				AllowedIPs:          []string{"10.10.0.2/32"},
				Endpoint:            "203.0.113.5:51820",
				PersistentKeepalive: 25,
			},
			{
				Name:       "phone",
				PublicKey:  "PHONEPUBLICKEY==",
				AllowedIPs: []string{"10.10.0.3/32", "fd00::3/128"},
			},
		},
	}

	out, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig returned error: %v", err)
	}

	mustContain := []string{
		"[Interface]",
		"PrivateKey = SERVERPRIVATEKEY==",
		"Address = 10.10.0.1/24, fd00::1/64",
		"ListenPort = 51820",
		"[Peer]",
		"# laptop",
		"PublicKey = LAPTOPPUBLICKEY==",
		"PresharedKey = PRESHAREDKEY==",
		"AllowedIPs = 10.10.0.2/32",
		"Endpoint = 203.0.113.5:51820",
		"PersistentKeepalive = 25",
		"# phone",
		"PublicKey = PHONEPUBLICKEY==",
		"AllowedIPs = 10.10.0.3/32, fd00::3/128",
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Errorf("expected generated config to contain %q, got:\n%s", want, out)
		}
	}

	// 兩個 [Peer] 區塊都要出現,順序跟輸入一致。
	if strings.Index(out, "# laptop") > strings.Index(out, "# phone") {
		t.Error("expected peers to appear in the order they were given")
	}
}

func TestGenerateConfig_OmitsOptionalFieldsWhenEmpty(t *testing.T) {
	cfg := Config{
		Interface: validInterface(),
		Peers: []PeerConfig{
			{Name: "minimal", PublicKey: "MINIMALPUBLICKEY==", AllowedIPs: []string{"10.10.0.9/32"}},
		},
	}

	out, err := GenerateConfig(cfg)
	if err != nil {
		t.Fatalf("GenerateConfig returned error: %v", err)
	}

	for _, mustNotContain := range []string{"PresharedKey = ", "Endpoint = ", "PersistentKeepalive = "} {
		if strings.Contains(out, mustNotContain) {
			t.Errorf("expected no %q line for a peer without that field set, got:\n%s", mustNotContain, out)
		}
	}
}

func TestGenerateConfig_RejectsInvalidConfig(t *testing.T) {
	_, err := GenerateConfig(Config{})
	if err == nil {
		t.Fatal("expected GenerateConfig to reject an invalid (empty) config")
	}
}
