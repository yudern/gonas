package api

import (
	"net"
	"net/http"

	"github.com/bng147/gonas/internal/state"
	"github.com/bng147/gonas/internal/wireguard"
)

// vpnInterfaceName 固定用 "wg0" 當介面名稱:GoNAS 目前只支援單一 WireGuard
// 介面(對一台家用/小型辦公室 NAS 來說,一個「回家」的 VPN 入口已經足夠,
// 多介面對應的是路由/多租戶這種複雜度,不在這個階段的範圍內),固定名稱
// 也讓 wg-quick 用的設定檔路徑可以是常數,不需要另外管理「介面名稱→
// 檔案路徑」的對應。
const vpnInterfaceName = "wg0"

func (s *Server) vpnConfPath() string { return s.dataDir + "/wireguard/" + vpnInterfaceName + ".conf" }

// vpnDefaultListenPort 是使用者沒有指定埠號時的預設值,跟 WireGuard 官方
// 文件、大多數用戶端 App 的預設埠一致,選一樣的值可以少讓使用者在防火牆/
// 路由器上多想一個「這個埠是什麼」。
const vpnDefaultListenPort = 51820

type vpnStatusResponse struct {
	Configured bool     `json:"configured"`
	Interface  string   `json:"interface,omitempty"`
	Address    []string `json:"address,omitempty"`
	ListenPort int      `json:"listenPort,omitempty"`
	PublicKey  string   `json:"publicKey,omitempty"` // 從私鑰算出來,方便使用者核對/分享,私鑰本身絕不出現在任何 API 回應裡
	PeerCount  int      `json:"peerCount"`
	Running    bool     `json:"running"`
	RawStatus  string   `json:"rawStatus,omitempty"`
	Warning    string   `json:"warning,omitempty"`
}

// handleVPNStatus 回傳目前 WireGuard 介面的設定摘要,並嘗試呼叫真正的
// `wg show` 確認介面是不是真的在跑。這台開發機沒有裝 wireguard-tools
// (見 internal/wireguard 套件開頭註解),所以 Status 呼叫失敗是預期中的
// 情況,這裡把它當成軟失敗處理:仍然回傳 200 跟已儲存的設定,只是
// Running=false 並附上 Warning 說明原因,而不是讓整支 API 回 500
// ——「WireGuard 有沒有設定」跟「wg-quick 這個外部工具在不在」是兩件
// 獨立的事,前者永遠該能查詢得到。
func (s *Server) handleVPNStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Snapshot().WireGuard
	if cfg == nil {
		writeJSON(w, http.StatusOK, vpnStatusResponse{Configured: false})
		return
	}

	resp := vpnStatusResponse{
		Configured: true,
		Interface:  vpnInterfaceName,
		Address:    cfg.Interface.Address,
		ListenPort: cfg.Interface.ListenPort,
		PeerCount:  len(cfg.Peers),
	}
	if pub, err := wireguard.PublicKeyFromPrivate(cfg.Interface.PrivateKey); err == nil {
		resp.PublicKey = pub
	}

	out, err := wireguard.Status(r.Context(), s.runner, vpnInterfaceName)
	if err != nil {
		resp.Warning = "無法查詢介面即時狀態(可能是尚未執行 `wg-quick up`,或這台主機沒有安裝 wireguard-tools): " + err.Error()
	} else {
		resp.Running = true
		resp.RawStatus = out
	}

	writeJSON(w, http.StatusOK, resp)
}

type vpnInterfaceRequest struct {
	Address    []string `json:"address"`
	ListenPort int      `json:"listenPort,omitempty"`
}

// handleVPNInterfaceSet 建立或更新 GoNAS 自己這端的 WireGuard 介面設定。
// 第一次呼叫(state.WireGuard 還是 nil)會產生一組全新的介面金鑰對;之後
// 重複呼叫只更新位址/埠號、刻意保留原本的私鑰不變 —— 悄悄輪替伺服器端
// 私鑰會讓所有已經核發出去的 client 設定檔全部失效(它們的 [Peer] 區塊
// 裡記的伺服器公鑰會對不上新私鑰算出來的公鑰),這種破壞性動作不該是
// 使用者呼叫「更新位址」時的意外副作用。
func (s *Server) handleVPNInterfaceSet(w http.ResponseWriter, r *http.Request) {
	var req vpnInterfaceRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.ListenPort == 0 {
		req.ListenPort = vpnDefaultListenPort
	}

	var resp vpnStatusResponse
	if err := s.store.Update(func(st *state.State) error {
		if st.WireGuard == nil {
			priv, _, err := wireguard.GenerateKeyPair()
			if err != nil {
				return err
			}
			st.WireGuard = &wireguard.Config{Interface: wireguard.InterfaceConfig{PrivateKey: priv}}
		}
		st.WireGuard.Interface.Address = req.Address
		st.WireGuard.Interface.ListenPort = req.ListenPort
		if err := st.WireGuard.Validate(); err != nil {
			return err
		}

		pub, err := wireguard.PublicKeyFromPrivate(st.WireGuard.Interface.PrivateKey)
		if err != nil {
			return err
		}
		resp = vpnStatusResponse{
			Configured: true,
			Interface:  vpnInterfaceName,
			Address:    st.WireGuard.Interface.Address,
			ListenPort: st.WireGuard.Interface.ListenPort,
			PublicKey:  pub,
			PeerCount:  len(st.WireGuard.Peers),
		}
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleVPNPeersList 只回傳已經存進 state 裡的 peer 清單 —— 這些欄位全部
// 都是公開資訊(名稱、公鑰、允許的 IP),沒有任何私鑰,可以直接序列化
// 回傳,不需要額外過濾。
func (s *Server) handleVPNPeersList(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Snapshot().WireGuard
	if cfg == nil {
		writeJSON(w, http.StatusOK, []wireguard.PeerConfig{})
		return
	}
	writeJSON(w, http.StatusOK, cfg.Peers)
}

type addPeerRequest struct {
	Name                string   `json:"name"`
	AllowedIPs          []string `json:"allowedIPs"`
	Endpoint            string   `json:"endpoint,omitempty"`            // GoNAS 對外可連到的位址(例如 DDNS 網域:埠號),寫進產生的用戶端設定檔裡,GoNAS 自己不需要用到這個值
	PersistentKeepalive int      `json:"persistentKeepalive,omitempty"` // 用戶端在 NAT 後面(絕大多數家用網路都是)時建議設 25 秒,見下方預設值
}

type addPeerResponse struct {
	Peer wireguard.PeerConfig `json:"peer"`
	// ClientConfig 是完整的 wg-quick 用戶端設定檔文字內容,只在建立當下
	// 回傳這一次、不會被持久化(GoNAS 只保留這個 peer 的公鑰)——這跟
	// SSH 私鑰、下載一次的 API token 是同一種「離開伺服器記憶體就沒有
	// 第二次機會拿到」的設計,使用者要自己把這份內容存到用戶端裝置上。
	ClientConfig string `json:"clientConfig"`
}

// defaultPeerKeepalive 是使用者沒有指定 PersistentKeepalive 時的預設值。
// 25 秒是 WireGuard 官方文件對「用戶端在 NAT 後面」情境的建議值 ——
// 沒有這個值,NAT 裝置上的連線對應(mapping)逾時後,伺服器主動送封包
// 給用戶端會因為 NAT 已經不認得這條路徑而送不到,家用網路幾乎必然
// 符合這個情境。
const defaultPeerKeepalive = 25

// handleVPNPeerAdd 幫一個新的用戶端裝置產生完整的金鑰對跟可以直接匯入的
// wg-quick 設定檔。伺服器端只留下這個 peer 的公鑰(GenerateKeyPair 產生
// 的私鑰只活在這次請求的記憶體裡,回應送出去之後就不存在了)——如果
// GoNAS 自己保留每個 peer 的私鑰,等於伺服器多了一份「所有用戶端裝置的
// 身分憑證」,萬一 state.json 外洩,受影響的就不只是伺服器自己這一端。
func (s *Server) handleVPNPeerAdd(w http.ResponseWriter, r *http.Request) {
	var req addPeerRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, errPeerNameRequired)
		return
	}
	if len(req.AllowedIPs) == 0 {
		writeError(w, http.StatusBadRequest, errPeerAllowedIPsEmpty)
		return
	}
	if req.PersistentKeepalive == 0 {
		req.PersistentKeepalive = defaultPeerKeepalive
	}

	serverCfg := s.store.Snapshot().WireGuard
	if serverCfg == nil {
		writeError(w, http.StatusConflict, errVPNNotConfigured)
		return
	}
	for _, p := range serverCfg.Peers {
		if p.Name == req.Name {
			writeError(w, http.StatusConflict, errPeerNameExists)
			return
		}
	}

	clientPriv, clientPub, err := wireguard.GenerateKeyPair()
	if err != nil {
		s.logger.Error("generating wireguard peer key pair failed", "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	serverPub, err := wireguard.PublicKeyFromPrivate(serverCfg.Interface.PrivateKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	peer := wireguard.PeerConfig{
		ID:                  newID(),
		Name:                req.Name,
		PublicKey:           clientPub,
		AllowedIPs:          req.AllowedIPs,
		PersistentKeepalive: req.PersistentKeepalive,
	}

	if err := s.store.Update(func(st *state.State) error {
		if st.WireGuard == nil {
			return errVPNNotConfigured
		}
		next := append([]wireguard.PeerConfig{}, st.WireGuard.Peers...)
		next = append(next, peer)
		candidate := *st.WireGuard
		candidate.Peers = next
		if err := candidate.Validate(); err != nil {
			return err
		}
		st.WireGuard.Peers = next
		return nil
	}); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	// 用戶端自己的介面位址就是它被分配到的 AllowedIPs(GoNAS 這裡採用
	// 最常見的「每個 peer 一個 /32」慣例,不單獨另外維護一份位址規劃)。
	// ListenPort 對只會主動連出去的用戶端裝置沒有實際作用,但
	// InterfaceConfig.Validate 要求填合法範圍內的值,固定用預設埠號即可
	// ——用戶端不需要對外開放這個埠,填什麼都不影響連線是否成功。
	clientCfg := wireguard.Config{
		Interface: wireguard.InterfaceConfig{
			PrivateKey: clientPriv,
			Address:    req.AllowedIPs,
			ListenPort: vpnDefaultListenPort,
		},
		Peers: []wireguard.PeerConfig{
			{
				Name:                "gonas",
				PublicKey:           serverPub,
				AllowedIPs:          networkCIDRs(serverCfg.Interface.Address),
				Endpoint:            req.Endpoint,
				PersistentKeepalive: req.PersistentKeepalive,
			},
		},
	}
	clientConfText, err := wireguard.GenerateConfig(clientCfg)
	if err != nil {
		// 這裡失敗代表 peer 已經寫進 state 了,但沒辦法回傳可用的設定檔
		// 內容給使用者 —— 不倒回剛剛的寫入(那個 peer 的公鑰本身是合法
		// 且已經生效的),只是誠實回報這次「順便產生設定檔文字」的動作
		// 失敗,讓使用者知道要用其他方式(例如手動組裝)拿到用戶端設定。
		s.logger.Error("peer saved but rendering its client config failed", "peer", peer.Name, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusCreated, addPeerResponse{Peer: peer, ClientConfig: clientConfText})
}

// networkCIDRs 把介面位址(例如 "10.10.0.1/24",主機位址搭配網段遮罩)
// 轉成用戶端設定檔 [Peer] 區塊該填的網段寫法(例如 "10.10.0.0/24")——
// AllowedIPs 在 WireGuard 裡的語意是「這個網段的流量走這個 peer」,填一個
// 帶著主機位元的位址雖然大多數實作會自動遮罩,但寫進設定檔給使用者看的
// 內容本來就該是正確的網段表示法,不該依賴讀取端幫忙善後。剖析失敗
// (理論上不會發生,因為 InterfaceConfig.Validate 已經檔過一次)就照原樣
// 傳回,不讓一個非預期的格式問題擋掉整個 API 呼叫。
func networkCIDRs(addrs []string) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		_, ipnet, err := net.ParseCIDR(a)
		if err != nil {
			out[i] = a
			continue
		}
		out[i] = ipnet.String()
	}
	return out
}

// handleVPNPeerDelete 移除一個 peer。伺服器端從來沒有保留過它的私鑰,
// 所以這個動作是不可逆的「撤銷存取權」,而不是「暫時停用、之後還能還原」
// ——使用者如果要讓同一台裝置重新連線,得重新走一次 handleVPNPeerAdd
// 產生新的金鑰對跟設定檔。
func (s *Server) handleVPNPeerDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	found := false
	if err := s.store.Update(func(st *state.State) error {
		if st.WireGuard == nil {
			return errVPNNotConfigured
		}
		kept := st.WireGuard.Peers[:0]
		for _, p := range st.WireGuard.Peers {
			if p.ID == id {
				found = true
				continue
			}
			kept = append(kept, p)
		}
		st.WireGuard.Peers = kept
		return nil
	}); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errPeerNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
