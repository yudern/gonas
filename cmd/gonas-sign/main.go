package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const usage = `gonas-sign — GoNAS 自我更新簽章工具

用法:
  gonas-sign keygen  [-out-dir DIR]
        產生一對新的 ed25519 金鑰。私鑰寫到 <DIR>/gonas-update-private.key
        (權限 0600,請妥善保管、不要外流、不要放進 git),並印出要嵌進
        gonasd 的公鑰 hex。

  gonas-sign pubkey  -key PRIVATE_KEY_FILE
        從私鑰檔導出公鑰 hex(就是編譯 gonasd 時 MANIFEST_PUBKEY 要用的值)。

  gonas-sign sign    -key PRIVATE_KEY_FILE -manifest MANIFEST.json [-out SIG_FILE]
        對 manifest 檔簽名,產出 detached 簽章檔(預設 <MANIFEST>.sig)。
        把 manifest 與這個 .sig 放到同一個目錄下對外提供即可。

  gonas-sign verify  -pub PUBKEY_HEX -manifest MANIFEST.json [-sig SIG_FILE]
        用公鑰驗證簽章(gonasd 驗證邏輯的鏡像),發佈前自我確認用。

  gonas-sign checksum FILE...
        印出每個檔案的 SHA256 hex,填進 manifest 的 asset.sha256 欄位用。

典型流程:
  1) gonas-sign keygen                       # 產生金鑰,記下印出的公鑰 hex
  2) make build-amd64 MANIFEST_PUBKEY=<公鑰> # 編出「會強制驗簽」的 gonasd
  3) 寫好 manifest.json(用 gonas-sign checksum 填每個 asset 的 sha256)
  4) gonas-sign sign -key gonas-update-private.key -manifest manifest.json
  5) 把 manifest.json 與 manifest.json.sig 一起放上你的 HTTPS 伺服器
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		cmdKeygen(os.Args[2:])
	case "pubkey":
		cmdPubkey(os.Args[2:])
	case "sign":
		cmdSign(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "checksum":
		cmdChecksum(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "gonas-sign: 未知的子指令 %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gonas-sign: "+err.Error())
	os.Exit(1)
}

func cmdKeygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	outDir := fs.String("out-dir", ".", "輸出金鑰檔的目錄")
	_ = fs.Parse(args)

	priv, pubHex, err := generateKeypair()
	if err != nil {
		fatal(err)
	}
	privPath := filepath.Join(*outDir, "gonas-update-private.key")
	if _, statErr := os.Stat(privPath); statErr == nil {
		fatal(fmt.Errorf("%s 已存在——拒絕覆蓋既有私鑰(覆蓋會讓已部署、嵌了舊公鑰的 gonasd 再也驗不過新簽章)。要重新產生請先手動移走舊檔", privPath))
	}
	if err := writeFileMode(privPath, []byte(encodePrivateKey(priv)+"\n"), 0o600); err != nil {
		fatal(fmt.Errorf("寫入私鑰檔: %w", err))
	}
	pubPath := filepath.Join(*outDir, "gonas-update-public.hex")
	if err := writeFileMode(pubPath, []byte(pubHex+"\n"), 0o644); err != nil {
		fatal(fmt.Errorf("寫入公鑰檔: %w", err))
	}

	fmt.Printf("已產生金鑰:\n")
	fmt.Printf("  私鑰(請保密,勿外流/勿進 git):%s\n", privPath)
	fmt.Printf("  公鑰 hex:%s\n\n", pubPath)
	fmt.Printf("公鑰:%s\n\n", pubHex)
	fmt.Printf("編譯「會強制驗簽」的 gonasd:\n")
	fmt.Printf("  make build-amd64 MANIFEST_PUBKEY=%s\n", pubHex)
}

func cmdPubkey(args []string) {
	fs := flag.NewFlagSet("pubkey", flag.ExitOnError)
	keyPath := fs.String("key", "", "私鑰檔路徑")
	_ = fs.Parse(args)
	if *keyPath == "" {
		fatal(fmt.Errorf("需要 -key 指定私鑰檔"))
	}
	data, err := os.ReadFile(*keyPath)
	if err != nil {
		fatal(fmt.Errorf("讀取私鑰檔: %w", err))
	}
	priv, err := decodePrivateKey(data)
	if err != nil {
		fatal(err)
	}
	fmt.Println(publicKeyHexFromPrivate(priv))
}

func cmdSign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "", "私鑰檔路徑")
	manifestPath := fs.String("manifest", "", "要簽名的 manifest.json 路徑")
	outPath := fs.String("out", "", "簽章輸出檔(預設 <manifest>.sig)")
	_ = fs.Parse(args)
	if *keyPath == "" || *manifestPath == "" {
		fatal(fmt.Errorf("需要 -key 與 -manifest"))
	}
	keyData, err := os.ReadFile(*keyPath)
	if err != nil {
		fatal(fmt.Errorf("讀取私鑰檔: %w", err))
	}
	priv, err := decodePrivateKey(keyData)
	if err != nil {
		fatal(err)
	}
	manifest, err := os.ReadFile(*manifestPath)
	if err != nil {
		fatal(fmt.Errorf("讀取 manifest: %w", err))
	}
	sig := signManifest(priv, manifest)
	sigPath := *outPath
	if sigPath == "" {
		sigPath = *manifestPath + ".sig"
	}
	if err := writeFileMode(sigPath, []byte(sig+"\n"), 0o644); err != nil {
		fatal(fmt.Errorf("寫入簽章檔: %w", err))
	}
	// 立刻用導出的公鑰自我驗證一次,確保產出的 .sig 一定驗得過(把「簽了但
	// 其實驗不過」這種只會在使用者機器上才爆的錯,擋在發佈前)。
	if err := verifyManifest(publicKeyHexFromPrivate(priv), manifest, sig); err != nil {
		fatal(fmt.Errorf("內部一致性檢查失敗(這不該發生): %w", err))
	}
	fmt.Printf("已簽名:%s\n", sigPath)
	fmt.Printf("把 %s 與 %s 放到同一個目錄對外提供。\n", filepath.Base(*manifestPath), filepath.Base(sigPath))
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	pubHex := fs.String("pub", "", "公鑰 hex")
	manifestPath := fs.String("manifest", "", "manifest.json 路徑")
	sigPath := fs.String("sig", "", "簽章檔(預設 <manifest>.sig)")
	_ = fs.Parse(args)
	if *pubHex == "" || *manifestPath == "" {
		fatal(fmt.Errorf("需要 -pub 與 -manifest"))
	}
	manifest, err := os.ReadFile(*manifestPath)
	if err != nil {
		fatal(fmt.Errorf("讀取 manifest: %w", err))
	}
	sp := *sigPath
	if sp == "" {
		sp = *manifestPath + ".sig"
	}
	sigData, err := os.ReadFile(sp)
	if err != nil {
		fatal(fmt.Errorf("讀取簽章檔: %w", err))
	}
	if err := verifyManifest(*pubHex, manifest, string(sigData)); err != nil {
		fatal(err)
	}
	fmt.Println("簽章驗證通過 ✓")
}

func cmdChecksum(args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("需要至少一個檔案路徑"))
	}
	for _, p := range args {
		data, err := os.ReadFile(p)
		if err != nil {
			fatal(fmt.Errorf("讀取 %s: %w", p, err))
		}
		fmt.Printf("%s  %s\n", sha256Hex(data), p)
	}
}
