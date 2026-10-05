package docker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// 鏡像更新檢查:比對「本機已拉的映像摘要」與「registry 上該 tag 現在的摘要」,
// 不一致就代表 registry 上有新版本(例如 :latest 被重新推送)。這是純讀取、
// 不下載任何層的輕量檢查(只 HEAD manifest),對應 Portainer/Watchtower 的做法。
//
// 注意(給真機驗證):這個檢查是 GoNAS 直接連 registry(registry-1.docker.io 等),
// 不走 dockerd 設定的鏡像加速器 —— 所以在完全連不上 Docker Hub 的網路,這個
// 檢查會失敗(回 error),UI 會顯示「無法檢查」。真正的「更新」動作走的是
// dockerd 的 pull(會用加速器),不受影響。零第三方依賴,純標準函式庫。

// ImageRef 是拆解後的映像位址。
type ImageRef struct {
	Registry string // 例如 registry-1.docker.io、ghcr.io、myreg:5000
	Repo     string // 例如 library/nginx、user/app、owner/app
	Tag      string // 例如 latest、1.25
	IsDigest bool   // ref 本身就是 @sha256:... 鎖定(不需要檢查更新)
}

const dockerHubRegistry = "registry-1.docker.io"

// ParseImageRef 把一個 docker 映像字串拆成 registry/repo/tag,補上 Docker Hub 的
// 預設(library/ 前綴、latest tag)。name@sha256:... 這種摘要鎖定的 ref 回
// IsDigest=true(已經釘死版本,沒有「新版本」的概念)。
func ParseImageRef(image string) ImageRef {
	ref := ImageRef{Registry: dockerHubRegistry, Tag: "latest"}

	name := image
	// 先切出 registry:第一段含有 "." 或 ":" 或等於 "localhost" 才算 registry host。
	if i := strings.IndexByte(name, '/'); i >= 0 {
		first := name[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			ref.Registry = first
			name = name[i+1:]
		}
	}

	// name 現在是 repo[:tag] 或 repo@sha256:...
	if i := strings.IndexByte(name, '@'); i >= 0 {
		ref.Repo = name[:i]
		ref.Tag = name[i+1:]
		ref.IsDigest = true
	} else if i := strings.LastIndexByte(name, ':'); i >= 0 {
		ref.Repo = name[:i]
		ref.Tag = name[i+1:]
	} else {
		ref.Repo = name
	}

	// Docker Hub 的單段名稱(nginx)實際上是 library/nginx。
	if ref.Registry == dockerHubRegistry && !strings.Contains(ref.Repo, "/") {
		ref.Repo = "library/" + ref.Repo
	}
	return ref
}

// manifestAccept 是查 manifest 時要帶的 Accept(涵蓋 v2 單一/清單與 OCI
// 單一/索引),這樣多架構映像也拿得到「清單摘要」—— 跟 dockerd 在多架構 pull
// 時存進 RepoDigests 的摘要是同一個,才比得起來。
const manifestAccept = "application/vnd.docker.distribution.manifest.v2+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.oci.image.index.v1+json"

// FetchRemoteDigest 查 registry 上某個 tag 現在的 manifest 摘要
// (Docker-Content-Digest header)。處理「先碰 401、讀 WWW-Authenticate、去
// realm 換 bearer token、再帶 token 重試」這套標準 token 認證流程。
func FetchRemoteDigest(ctx context.Context, client *http.Client, ref ImageRef) (string, error) {
	url := "https://" + ref.Registry + "/v2/" + ref.Repo + "/manifests/" + ref.Tag

	doHead := func(token string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", manifestAccept)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}

	resp, err := doHead("")
	if err != nil {
		return "", fmt.Errorf("querying registry %s: %w", ref.Registry, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		token, err := fetchBearerToken(ctx, client, challenge)
		if err != nil {
			return "", err
		}
		resp, err = doHead(token)
		if err != nil {
			return "", fmt.Errorf("querying registry %s (authed): %w", ref.Registry, err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry returned %s for %s:%s", resp.Status, ref.Repo, ref.Tag)
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("registry did not return a content digest for %s:%s", ref.Repo, ref.Tag)
	}
	return digest, nil
}

// fetchBearerToken 依 WWW-Authenticate 的 Bearer 挑戰(realm/service/scope)去
// 換一個 pull-only 的 bearer token。
func fetchBearerToken(ctx context.Context, client *http.Client, challenge string) (string, error) {
	realm, params := parseBearerChallenge(challenge)
	if realm == "" {
		return "", fmt.Errorf("registry asked for auth but gave no bearer realm")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching registry token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry token endpoint returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// token 回應是 {"token":"..."} 或 {"access_token":"..."}。
	tok := extractJSONString(body, "token")
	if tok == "" {
		tok = extractJSONString(body, "access_token")
	}
	if tok == "" {
		return "", fmt.Errorf("registry token response had no token")
	}
	return tok, nil
}

// parseBearerChallenge 解析 `Bearer realm="https://...",service="...",scope="..."`。
func parseBearerChallenge(h string) (realm string, params map[string]string) {
	params = map[string]string{}
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return "", params
	}
	for _, part := range splitTopLevelCommas(h[len("Bearer "):]) {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if key == "realm" {
			realm = val
		} else {
			params[key] = val
		}
	}
	return realm, params
}

// splitTopLevelCommas 以逗號切,但不切在引號內的逗號(scope 值可能含逗號)。
func splitTopLevelCommas(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// extractJSONString 從一小段 JSON 裡抓 "key":"value"(避免為了一個欄位引入
// 完整 struct 定義;token 回應就這一兩個欄位)。只處理字串值。
func extractJSONString(body []byte, key string) string {
	needle := `"` + key + `"`
	s := string(body)
	i := strings.Index(s, needle)
	if i < 0 {
		return ""
	}
	s = s[i+len(needle):]
	j := strings.IndexByte(s, ':')
	if j < 0 {
		return ""
	}
	s = strings.TrimSpace(s[j+1:])
	if len(s) == 0 || s[0] != '"' {
		return ""
	}
	s = s[1:]
	// 找到結尾引號(token 不含跳脫字元,簡單找下一個 ")。
	k := strings.IndexByte(s, '"')
	if k < 0 {
		return ""
	}
	return s[:k]
}

// LocalImageDigest 回傳本機該映像的 RepoDigest 摘要(sha256:...);映像不存在或
// 沒有 RepoDigest(例如本機 build、從未從 registry 拉過)回空字串、無錯誤。
func (c *Client) LocalImageDigest(ctx context.Context, image string) (string, error) {
	var out struct {
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := c.doJSON(ctx, "GET", "/images/"+pathEscapeImage(image)+"/json", nil, &out); err != nil {
		return "", err
	}
	for _, rd := range out.RepoDigests {
		if i := strings.IndexByte(rd, '@'); i >= 0 {
			return rd[i+1:], nil
		}
	}
	return "", nil
}

// pathEscapeImage 對映像名做 URL path 轉義(名稱可能含 / 與 :)。
func pathEscapeImage(image string) string {
	// Docker API 接受未轉義的 /;只需要處理不會出現在 name 的字元即可,
	// 這裡直接原樣(docker 映像名的合法字元集都是 URL path 安全的)。
	return image
}
