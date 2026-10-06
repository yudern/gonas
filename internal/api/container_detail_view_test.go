package api

import (
	"encoding/json"
	"testing"

	"github.com/bng147/gonas/internal/docker"
)

// 用一份真實格式(大寫欄位)的 docker inspect 片段,確認檢視欄位都有填上。
func TestContainerDetailView_FromDockerInspect(t *testing.T) {
	raw := `{"Id":"abc","Name":"/portainer-app","Created":"2026-10-06T10:00:00Z","Path":"/portainer","Args":["--http-enabled"],
	"Image":"sha256:1234","State":{"Status":"running","Running":true,"Pid":42,"StartedAt":"2026-10-06T10:00:01Z","FinishedAt":"0001-01-01T00:00:00Z",
	"Health":{"Status":"healthy","FailingStreak":0,"Log":[{"ExitCode":0,"Output":"ok\n"}]}},"RestartCount":2,
	"Config":{"Hostname":"f43a6764eb67","Image":"portainer/portainer-ce:latest","Env":["TZ=Asia/Shanghai","DB_PASSWORD=hunter2","PATH=/usr/bin"],
	"WorkingDir":"/","ExposedPorts":{"9000/tcp":{},"8000/tcp":{}},"Labels":{"com.gonas.app":"portainer"}},
	"HostConfig":{"Memory":536870912,"NanoCpus":1500000000,"NetworkMode":"bridge","RestartPolicy":{"Name":"unless-stopped"}},
	"Mounts":[{"Type":"bind","Source":"/mnt/tank/appdata/portainer","Destination":"/data","RW":true}],
	"NetworkSettings":{"Ports":{"9000/tcp":[{"HostIp":"0.0.0.0","HostPort":"9000"}]},
	"Networks":{"bridge":{"IPAddress":"172.17.0.2","IPPrefixLen":16,"Gateway":"172.17.0.1","MacAddress":"02:42:ac:11:00:02"}}}}`
	var d docker.ContainerDetail
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	v := newContainerDetailView(d)
	if v.Name != "portainer-app" || v.Image != "portainer/portainer-ce:latest" || v.RestartPolicy != "unless-stopped" || v.RestartCount != 2 {
		t.Errorf("basic fields: %+v", v)
	}
	if v.Command != "/portainer --http-enabled" || v.State.Health != "healthy" || v.State.FinishedAt != "" || v.State.Pid != 42 {
		t.Errorf("state/command: %+v", v.State)
	}
	if len(v.Env) != 3 || !v.Env[1].Secret || v.Env[0].Secret || v.Env[1].Value != "hunter2" {
		t.Errorf("env: %+v", v.Env)
	}
	if len(v.Networks) != 1 || v.Networks[0].IP != "172.17.0.2/16" || v.Networks[0].Gateway != "172.17.0.1" || v.Networks[0].MAC == "" {
		t.Errorf("networks: %+v", v.Networks)
	}
	if len(v.Ports) != 2 || v.Ports[1].HostPort != "9000" || v.Ports[0].Container != "8000/tcp" || v.Ports[0].HostPort != "" {
		t.Errorf("ports: %+v", v.Ports)
	}
	if len(v.Mounts) != 1 || v.Mounts[0].Destination != "/data" || !v.Mounts[0].RW {
		t.Errorf("mounts: %+v", v.Mounts)
	}
}
