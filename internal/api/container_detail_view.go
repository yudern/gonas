package api

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bng147/gonas/internal/docker"
)

// 第六十六輪(使用者:「容器詳情顯示東西太少了,環境變量、IP 等等沒有顯示」)。
// 根因:handler 原樣回傳 Docker inspect 的大寫欄位(Config/State/HostConfig…),
// 前端卻讀小寫(d.config/d.state…),幾乎每一欄都是 undefined,只剩下有預設
// 文字的「記憶體/CPU 上限」。這裡改成回一份扁平、小寫、前端直接能用的檢視,
// 並把內容補齊:狀態/健康檢查、啟動指令、環境變數(敏感值標記)、埠對應、
// 掛載、每個網路的 IP/閘道/MAC、資源與權限、標籤。

type containerDetailView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageID       string            `json:"imageId"`
	Created       string            `json:"created"`
	Platform      string            `json:"platform,omitempty"`
	State         stateView         `json:"state"`
	RestartCount  int               `json:"restartCount"`
	RestartPolicy string            `json:"restartPolicy"`
	Command       string            `json:"command"`
	Entrypoint    string            `json:"entrypoint,omitempty"`
	WorkingDir    string            `json:"workingDir,omitempty"`
	User          string            `json:"user,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	Env           []envView         `json:"env"`
	Ports         []portView        `json:"ports"`
	Mounts        []mountView       `json:"mounts"`
	NetworkMode   string            `json:"networkMode"`
	Networks      []netView         `json:"networks"`
	Memory        int64             `json:"memory"`
	NanoCPUs      int64             `json:"nanoCpus"`
	Privileged    bool              `json:"privileged"`
	CapAdd        []string          `json:"capAdd,omitempty"`
	Devices       []string          `json:"devices,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type stateView struct {
	Status        string `json:"status"`
	Running       bool   `json:"running"`
	OOMKilled     bool   `json:"oomKilled,omitempty"`
	Pid           int    `json:"pid,omitempty"`
	ExitCode      int    `json:"exitCode"`
	StartedAt     string `json:"startedAt,omitempty"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	Error         string `json:"error,omitempty"`
	Health        string `json:"health,omitempty"`
	HealthFailing int    `json:"healthFailing,omitempty"`
	HealthLast    string `json:"healthLast,omitempty"`
}

type envView struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Secret bool   `json:"secret,omitempty"`
}

type portView struct {
	Container string `json:"container"`
	HostIP    string `json:"hostIp,omitempty"`
	HostPort  string `json:"hostPort,omitempty"`
}

type mountView struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

type netView struct {
	Name    string   `json:"name"`
	IP      string   `json:"ip,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	MAC     string   `json:"mac,omitempty"`
	IPv6    string   `json:"ipv6,omitempty"`
	Aliases []string `json:"aliases,omitempty"`
}

// secretEnvRe:名字看起來像密碼/金鑰的環境變數,前端預設遮住、點一下才顯示。
var secretEnvRe = regexp.MustCompile(`(?i)(pass|secret|token|key|credential|auth|private)`)

// zeroTime 是 Docker 對「從沒發生過」的時間戳的表示。
const zeroTime = "0001-01-01T00:00:00Z"

func nonZeroTime(s string) string {
	if s == "" || s == zeroTime {
		return ""
	}
	return s
}

func shellJoin(parts []string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, " \t\"'") {
			out = append(out, strconv.Quote(p))
		} else {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

func newContainerDetailView(d docker.ContainerDetail) containerDetailView {
	v := containerDetailView{
		ID:            d.ID,
		Name:          strings.TrimPrefix(d.Name, "/"),
		Image:         d.Config.Image,
		ImageID:       d.Image,
		Created:       d.Created,
		Platform:      d.Platform,
		RestartCount:  d.RestartCount,
		RestartPolicy: d.HostConfig.RestartPolicy.Name,
		Command:       shellJoin(append([]string{d.Path}, d.Args...)),
		Entrypoint:    shellJoin(d.Config.Entrypoint),
		WorkingDir:    d.Config.WorkingDir,
		User:          d.Config.User,
		Hostname:      d.Config.Hostname,
		NetworkMode:   d.HostConfig.NetworkMode,
		Memory:        d.HostConfig.Memory,
		NanoCPUs:      d.HostConfig.NanoCpus,
		Privileged:    d.HostConfig.Privileged,
		CapAdd:        d.HostConfig.CapAdd,
		Labels:        d.Config.Labels,
		Env:           []envView{},
		Ports:         []portView{},
		Mounts:        []mountView{},
		Networks:      []netView{},
	}
	if v.RestartPolicy == "" {
		v.RestartPolicy = "no"
	}
	if d.HostConfig.RestartPolicy.Name == "on-failure" && d.HostConfig.RestartPolicy.MaximumRetryCount > 0 {
		v.RestartPolicy += ":" + strconv.Itoa(d.HostConfig.RestartPolicy.MaximumRetryCount)
	}
	st := d.State
	v.State = stateView{
		Status: st.Status, Running: st.Running, OOMKilled: st.OOMKilled, Pid: st.Pid, ExitCode: st.ExitCode,
		StartedAt: nonZeroTime(st.StartedAt), FinishedAt: nonZeroTime(st.FinishedAt), Error: st.Error,
	}
	if st.Health != nil {
		v.State.Health = st.Health.Status
		v.State.HealthFailing = st.Health.FailingStreak
		if n := len(st.Health.Log); n > 0 {
			out := strings.TrimSpace(st.Health.Log[n-1].Output)
			if len(out) > 300 {
				out = out[:300] + "…"
			}
			v.State.HealthLast = out
		}
	}
	for _, e := range d.Config.Env {
		k, val, _ := strings.Cut(e, "=")
		v.Env = append(v.Env, envView{Key: k, Value: val, Secret: secretEnvRe.MatchString(k)})
	}
	// 埠:有對應到宿主的列出宿主位址/埠;只有 EXPOSE、沒對應的也列出(hostPort 空)。
	seen := map[string]bool{}
	for cp, binds := range d.NetworkSettings.Ports {
		seen[cp] = true
		if len(binds) == 0 {
			v.Ports = append(v.Ports, portView{Container: cp})
			continue
		}
		for _, b := range binds {
			v.Ports = append(v.Ports, portView{Container: cp, HostIP: b.HostIP, HostPort: b.HostPort})
		}
	}
	for cp := range d.Config.ExposedPorts {
		if !seen[cp] {
			v.Ports = append(v.Ports, portView{Container: cp})
		}
	}
	sort.Slice(v.Ports, func(i, j int) bool {
		if v.Ports[i].Container != v.Ports[j].Container {
			return v.Ports[i].Container < v.Ports[j].Container
		}
		return v.Ports[i].HostIP < v.Ports[j].HostIP
	})
	for _, m := range d.Mounts {
		src := m.Source
		if m.Type == "volume" && m.Name != "" {
			src = m.Name + " (" + m.Source + ")"
		}
		v.Mounts = append(v.Mounts, mountView{Type: m.Type, Source: src, Destination: m.Destination, RW: m.RW})
	}
	for name, n := range d.NetworkSettings.Networks {
		ip := n.IPAddress
		if ip != "" && n.IPPrefixLen > 0 {
			ip += "/" + strconv.Itoa(n.IPPrefixLen)
		}
		v.Networks = append(v.Networks, netView{Name: name, IP: ip, Gateway: n.Gateway, MAC: n.MacAddress, IPv6: n.GlobalIPv6Address, Aliases: n.Aliases})
	}
	sort.Slice(v.Networks, func(i, j int) bool { return v.Networks[i].Name < v.Networks[j].Name })
	for _, dv := range d.HostConfig.Devices {
		v.Devices = append(v.Devices, dv.PathOnHost+" → "+dv.PathInContainer)
	}
	return v
}
