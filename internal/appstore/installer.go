package appstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bng147/gonas/internal/docker"
)

// ServiceOverride 是使用者在安裝精靈裡針對某個服務實際填的值,
// 用來覆蓋/補齊範本裡沒有預設值的部分(尤其是 VolumeHostPaths —— 範本
// 刻意不預設資料要落在哪顆碟,詳見 template.go 的說明)。
// json tag 明確用小寫 —— 前端送上來的就是 env/volumeHostPaths/portHostOverrides,
// 讀回來(GET installedApps 的 overrides)也要是同樣的小寫 key,前端編輯表單才
// 讀得到既有值(第六十輪:無 tag 時 Go 會以欄位名大寫輸出,造成讀寫 key 不一致)。
type ServiceOverride struct {
	Env               map[string]string `json:"env,omitempty"`               // env key -> 使用者填的值
	VolumeHostPaths   map[string]string `json:"volumeHostPaths,omitempty"`   // containerPath -> 使用者選的陣列路徑
	PortHostOverrides map[int]int       `json:"portHostOverrides,omitempty"` // containerPort -> 使用者改過的 hostPort
	// Image 讓使用者在安裝時改掉這個服務要拉的映像位址 —— 最常見用途是把
	// docker.io 的映像換成國內鏡像源(例如把 nginx:latest 改成
	// docker.m.daocloud.io/library/nginx:latest),對 Docker Hub 連不上/很慢的
	// 網路環境(中國)很實用。留空則用範本原本的 Image。
	Image string `json:"image,omitempty"`
}

// EffectiveImage 回傳這個服務實際要用的映像位址:有覆寫就用覆寫的,否則用
// 範本預設。集中在一處,避免 Install/Update 各自判斷不一致。
func (o ServiceOverride) EffectiveImage(templateImage string) string {
	if strings.TrimSpace(o.Image) != "" {
		return strings.TrimSpace(o.Image)
	}
	return templateImage
}

// InstallRequest 是安裝一個 App 所需的完整輸入。
type InstallRequest struct {
	Template AppTemplate
	// Overrides 依服務名稱(ServiceTemplate.Name)對應各自的使用者輸入,
	// 沒填的服務就完全套用範本預設值。
	Overrides map[string]ServiceOverride
	// OnPullProgress 是選用的拉取進度回呼，可為 nil。
	OnPullProgress func(serviceName, status string)
	// OnRollbackError 是選用的回呼:安裝失敗後回滾(停止/移除已建立的容器、
	// 移除網路)過程中若有錯誤,透過它回報。可為 nil。第三十四輪加上——
	// 原本回滾清理失敗是直接丟棄(installer 這層沒有 logger),導致「安裝
	// 失敗、回滾又沒清乾淨」時完全無跡可尋;呼叫端(internal/api)接上
	// 自己的 logger 就能記下來。
	OnRollbackError func(serviceName string, err error)
	// ForcePull 為 true 時,即使本機已有該 image 也強制重拉(給「更新 App」用:
	// 重拉同一個 tag 以取得 registry 上的新版本)。預設 false(安裝時本機有就用)。
	ForcePull bool
	// StartGracePeriod 是「啟動容器後、回頭確認它還活著」之前等待的時間。
	// 第六十輪(QA 覆核):原本 Install 呼叫 StartContainer 後就無條件回報成功,
	// 一個因設定錯誤(env 缺、image entrypoint 掛)開機即崩潰的容器也會被當成
	// 「安裝成功」,UI 顯示成功但東西根本沒起來。裝好後等一小段再 Inspect 一次
	// 抓「馬上就崩掉」的情況。0 代表不等待、直接 Inspect(測試用)。
	StartGracePeriod time.Duration
	// EnsureHostDir 在建立容器前,確保每個 bind 掛載的「宿主端目錄」存在。
	// 第六十輪(使用者需求:路徑不存在時直接自動建立再安裝)——原本若使用者
	// 填的 appdata 路徑還不存在,就只能靠 docker daemon 自動建(建出來是
	// root:root、有時還會出權限問題),而且對「先建目錄再掛」的期待不明確。
	// 這裡在掛載前明確 MkdirAll,冪等(已存在不報錯),讓「填一個還沒建的
	// 路徑」也能一鍵裝起來。可為 nil,呼叫端不設就用 os.MkdirAll(0o755)。
	// 抽成 hook 是為了讓 installer 測試不必真的去動檔案系統。
	EnsureHostDir func(path string) error
}

// InstallResult 記錄安裝完成後每個服務對應到的容器 ID，方便呼叫端記錄下來
// (例如寫進 Web UI 的「已安裝 App」清單）。
type InstallResult struct {
	ContainerIDs map[string]string `json:"containerIds"` // service name -> container id
	NetworkID    string            `json:"networkId"`    // 多服務 App 才會非空
}

// Install 把範本翻譯成實際的 Docker 資源並啟動起來：需要的話先建立專屬網路、
// 依序拉取映像、建立並啟動每個服務的容器。任何一步失敗，會盡力把這次呼叫
// 已經建立的容器/網路清乾淨再回傳錯誤 —— 不留下「裝到一半」的殘骸,
// 讓使用者可以直接重試而不必先手動清理。
func Install(ctx context.Context, client *docker.Client, req InstallRequest) (InstallResult, error) {
	if err := req.Template.Validate(); err != nil {
		return InstallResult{}, fmt.Errorf("invalid app template: %w", err)
	}

	result := InstallResult{ContainerIDs: make(map[string]string, len(req.Template.Services))}
	var createdNetwork bool

	reportRollback := func(svcName string, err error) {
		if err != nil && req.OnRollbackError != nil {
			req.OnRollbackError(svcName, err)
		}
	}
	rollback := func() {
		for svcName, id := range result.ContainerIDs {
			_ = client.StopContainer(ctx, id, 5)
			// 盡力而為的清理,但清理失敗不再直接丟棄——透過 OnRollbackError
			// 回報給呼叫端(通常接上 logger),避免「安裝失敗、回滾又沒清乾淨」
			// 時完全無跡可尋。呼叫端仍會拿到原始的安裝失敗錯誤當回傳值。
			if err := client.RemoveContainer(ctx, id, true); err != nil {
				reportRollback(svcName, fmt.Errorf("removing container for service %q: %w", svcName, err))
			}
		}
		if createdNetwork {
			if err := client.RemoveNetwork(ctx, docker.AppNetworkName(req.Template.ID)); err != nil {
				reportRollback("", fmt.Errorf("removing app network: %w", err))
			}
		}
	}

	networkMode := ""
	if len(req.Template.Services) > 1 {
		netID, err := client.EnsureAppNetwork(ctx, req.Template.ID)
		if err != nil {
			return InstallResult{}, fmt.Errorf("preparing network for app %q: %w", req.Template.ID, err)
		}
		result.NetworkID = netID
		networkMode = docker.AppNetworkName(req.Template.ID)
		createdNetwork = true // 就算是重用既有網路也一併視為「這次安裝擁有它」，Uninstall 時會清掉
	}

	for _, svc := range req.Template.Services {
		override := req.Overrides[svc.Name]
		image := override.EffectiveImage(svc.Image) // 可能被使用者改成國內鏡像源

		env, err := ResolveEnv(svc, override.Env)
		if err != nil {
			rollback()
			return InstallResult{}, fmt.Errorf("service %q: %w", svc.Name, err)
		}

		mounts, err := resolveMounts(svc, override.VolumeHostPaths)
		if err != nil {
			rollback()
			return InstallResult{}, fmt.Errorf("service %q: %w", svc.Name, err)
		}

		// 第六十輪:掛載前確保每個宿主端 bind 目錄存在(路徑不存在就自動建),
		// 讓使用者填一個還沒建的 appdata 路徑也能直接裝起來。冪等。
		ensureDir := req.EnsureHostDir
		if ensureDir == nil {
			ensureDir = func(p string) error { return os.MkdirAll(p, 0o755) }
		}
		for _, m := range mounts {
			// 只處理絕對路徑的 bind 掛載(named volume 之類不是本機路徑,跳過)。
			if !filepath.IsAbs(m.HostPath) {
				continue
			}
			if err := ensureDir(m.HostPath); err != nil {
				rollback()
				return InstallResult{}, fmt.Errorf("service %q: creating host directory %q: %w", svc.Name, m.HostPath, err)
			}
		}

		ports := resolvePorts(svc, override.PortHostOverrides)

		// 先看本機是不是已經有這個 image，有的話就不用去 registry —— 這對
		// 網路受限的環境、或是使用者自建的本機映像檔(RepoTag 不在任何
		// registry 上)特別重要,詳見 docker.ImageExists 的說明。
		// 第六十輪:ForcePull 時強制重拉(即使本機已有),這是「更新 App」要的——
		// 同一個 tag(例如 :latest)在 registry 上可能已經是新版本了。
		exists := false
		if !req.ForcePull {
			var err error
			exists, err = client.ImageExists(ctx, image)
			if err != nil {
				rollback()
				return InstallResult{}, fmt.Errorf("service %q: %w", svc.Name, err)
			}
		}
		if exists {
			if req.OnPullProgress != nil {
				req.OnPullProgress(svc.Name, "image already present locally, skipping pull")
			}
		} else {
			progress := func(status string) {
				if req.OnPullProgress != nil {
					req.OnPullProgress(svc.Name, status)
				}
			}
			if err := client.PullImage(ctx, image, progress); err != nil {
				rollback()
				return InstallResult{}, fmt.Errorf("service %q: pulling image %q: %w", svc.Name, image, err)
			}
		}

		containerName := req.Template.ID + "-" + svc.Name
		id, _, err := client.CreateContainer(ctx, docker.CreateContainerRequest{
			Name:          containerName,
			Image:         image,
			Cmd:           svc.Command,
			Env:           env,
			Ports:         ports,
			Mounts:        mounts,
			RestartPolicy: svc.RestartPolicy,
			NetworkMode:   networkMode,
			Labels: map[string]string{
				"com.gonas.app":     req.Template.ID,
				"com.gonas.service": svc.Name,
			},
		})
		if err != nil {
			rollback()
			return InstallResult{}, fmt.Errorf("service %q: %w", svc.Name, err)
		}
		result.ContainerIDs[svc.Name] = id

		if err := client.StartContainer(ctx, id); err != nil {
			rollback()
			return InstallResult{}, fmt.Errorf("service %q: starting container: %w", svc.Name, err)
		}

		// 第六十輪:裝好後確認容器沒有「開機即崩潰」。best-effort——Inspect 失敗
		// (連不到/舊 daemon 不支援)不擋安裝(避免把「其實裝好了、只是查不到」
		// 誤判成失敗);只有「Inspect 成功、而且明確看到它已經退出」才視為安裝
		// 失敗並回滾,把 exit code 一併回報,讓使用者知道是這個容器起不來。
		if req.StartGracePeriod > 0 {
			select {
			case <-time.After(req.StartGracePeriod):
			case <-ctx.Done():
			}
		}
		if insp, err := client.InspectContainer(ctx, id); err == nil {
			if !insp.State.Running && insp.State.Status == "exited" {
				rollback()
				return InstallResult{}, fmt.Errorf("service %q: container exited right after starting (exit code %d) — check its configuration/logs", svc.Name, insp.State.ExitCode)
			}
		}
	}

	return result, nil
}

// ValidateInstallable 確認「這份範本 + 使用者當初填的覆寫」真的裝得起來——
// 尤其是必填 env 有值、每個掛載都有宿主路徑。第六十輪:給「更新 App」在
// 「移除舊容器之前」先擋掉會失敗的情況,才不會把 App 拆了卻裝不回去。
func ValidateInstallable(tmpl AppTemplate, overrides map[string]ServiceOverride) error {
	if err := tmpl.Validate(); err != nil {
		return err
	}
	for _, svc := range tmpl.Services {
		ov := overrides[svc.Name]
		if _, err := ResolveEnv(svc, ov.Env); err != nil {
			return fmt.Errorf("service %q: %w", svc.Name, err)
		}
		if _, err := resolveMounts(svc, ov.VolumeHostPaths); err != nil {
			return fmt.Errorf("service %q: %w", svc.Name, err)
		}
	}
	return nil
}

// Update 用同一份範本 + 覆寫「重拉映像並重建容器」,保留 bind 掛載的宿主資料
// (更新 App 到新版本的動作)。順序刻意是「先拉、再換」以確保安全:
//
//  1. 先驗證裝得起來(ValidateInstallable)——必填 env/掛載都齊,不齊就直接
//     回錯,完全不動正在跑的 App。
//  2. 把所有服務的新映像先拉下來(舊 App 還在跑)。這一步最可能因為網路/
//     registry 失敗——失敗就中止,舊 App 原封不動,使用者沒有任何損失。
//  3. 映像都到手後,才移除舊容器/網路(bind 掛載的宿主資料不會被刪),
//     再用剛拉下來的本機映像重建(此時不需要再 ForcePull)。
//
// 這樣「更新失敗」在絕大多數情況下 = 「還是舊版本、東西照跑」,而不是把 App
// 弄不見。第三、四步之間的視窗很短、且都是本機操作,失敗機率低。
func Update(ctx context.Context, client *docker.Client, req InstallRequest) (InstallResult, error) {
	if err := ValidateInstallable(req.Template, req.Overrides); err != nil {
		return InstallResult{}, err
	}
	for _, svc := range req.Template.Services {
		image := req.Overrides[svc.Name].EffectiveImage(svc.Image)
		progress := func(status string) {
			if req.OnPullProgress != nil {
				req.OnPullProgress(svc.Name, status)
			}
		}
		if err := client.PullImage(ctx, image, progress); err != nil {
			return InstallResult{}, fmt.Errorf("pulling new image for service %q (%s): %w — the app was left running on its current version", svc.Name, image, err)
		}
	}
	if err := Uninstall(ctx, client, req.Template.ID); err != nil {
		return InstallResult{}, fmt.Errorf("removing the old version before update: %w", err)
	}
	req.ForcePull = false // 剛才已經逐一拉過新映像,重建時直接用本機的即可
	return Install(ctx, client, req)
}

// Uninstall 找出所有標記為屬於這個 App 的容器並停止、移除，多服務 App
// 也會一併移除專屬網路。刻意用 Docker 標籤(com.gonas.app)重新查詢,
// 而不是要求呼叫端自己保存容器 ID 清單 —— 就算 GoNAS daemon 重開機、
// 記憶體裡的狀態遺失了，也還能靠標籤把整個 App 找回來清乾淨。
func Uninstall(ctx context.Context, client *docker.Client, appID string) error {
	containers, err := client.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("listing containers to uninstall app %q: %w", appID, err)
	}

	var firstErr error
	recordErr := func(err error) {
		if firstErr == nil {
			firstErr = err
		}
	}

	removedAny := false
	for _, c := range containers {
		if c.Labels["com.gonas.app"] != appID {
			continue
		}
		removedAny = true
		_ = client.StopContainer(ctx, c.ID, 10) // 停止失敗也繼續嘗試強制移除，不要因此卡住整個解除安裝
		if err := client.RemoveContainer(ctx, c.ID, true); err != nil {
			recordErr(fmt.Errorf("removing container %s (app %q): %w", c.ID, appID, err))
		}
	}

	if removedAny {
		// 先查一次網路是不是真的存在，而不是直接呼叫 RemoveNetwork 再吞掉
		// 「找不到」的錯誤 —— 單服務 App 從來沒建立過專屬網路是完全正常的情況,
		// 不該讓 Uninstall 對這種最常見的案例回報一個其實無害的錯誤。
		networks, err := client.ListNetworks(ctx)
		if err != nil {
			recordErr(fmt.Errorf("checking for app network before removal (app %q): %w", appID, err))
		} else {
			name := docker.AppNetworkName(appID)
			for _, n := range networks {
				if n.Name != name {
					continue
				}
				if err := client.RemoveNetwork(ctx, n.ID); err != nil {
					recordErr(fmt.Errorf("removing network %q for app %q: %w", name, appID, err))
				}
				break
			}
		}
	}

	return firstErr
}

func resolveMounts(svc ServiceTemplate, hostPaths map[string]string) ([]docker.Mount, error) {
	mounts := make([]docker.Mount, 0, len(svc.Volumes))
	for _, v := range svc.Volumes {
		hostPath := v.HostPath
		if override, ok := hostPaths[v.ContainerPath]; ok && override != "" {
			hostPath = override
		}
		if hostPath == "" {
			return nil, fmt.Errorf("volume %q has no host path: this template requires the installer to choose one on the array", v.ContainerPath)
		}
		mounts = append(mounts, docker.Mount{
			HostPath:      hostPath,
			ContainerPath: v.ContainerPath,
			ReadOnly:      v.ReadOnly,
		})
	}
	return mounts, nil
}

func resolvePorts(svc ServiceTemplate, overrides map[int]int) []docker.PortSpec {
	ports := make([]docker.PortSpec, 0, len(svc.Ports))
	for _, p := range svc.Ports {
		hostPort := p.HostPort
		if override, ok := overrides[p.ContainerPort]; ok && override != 0 {
			hostPort = override
		}
		ports = append(ports, docker.PortSpec{
			ContainerPort: p.ContainerPort,
			HostPort:      hostPort,
			Protocol:      p.Protocol,
		})
	}
	return ports
}
