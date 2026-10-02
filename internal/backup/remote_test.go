package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// recordingRunner 只記錄每次 Run 呼叫,不模擬檔案複製 —— 異地鏡像的目的地是
// user@host:path 這種遠端規格,拿去 os.MkdirAll 只會在本機建出奇怪的目錄,對
// 「驗證指令組裝」這件事也沒有幫助。failErr 非 nil 時模擬 rsync 失敗。
type recordingRunner struct {
	calls   []call
	failErr error
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{name: name, args: args})
	return nil, r.failErr
}

func (r *recordingRunner) RunWithStdin(ctx context.Context, _ []byte, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}

// TestBuildRemoteSSHOpt_DefaultsPortAndOmitsKey 驗證沒帶 port / sshKey 時的 -e 字串:
// port 退回 22、不帶 -i,且一定帶 BatchMode=yes(需要密碼時直接失敗而非卡住)。
func TestBuildRemoteSSHOpt_DefaultsPortAndOmitsKey(t *testing.T) {
	got := buildRemoteSSHOpt(&RemoteDest{Host: "h", User: "u", Path: "/x"})
	for _, want := range []string{"ssh -p 22", "BatchMode=yes", "StrictHostKeyChecking=accept-new", "ConnectTimeout=20"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected ssh opt to contain %q, got %q", want, got)
		}
	}
	if strings.Contains(got, " -i ") {
		t.Errorf("expected no -i when SSHKey is empty, got %q", got)
	}
}

// TestBuildRemoteSSHOpt_CustomPortAndKey 驗證有帶 port / sshKey 時兩者都進字串。
func TestBuildRemoteSSHOpt_CustomPortAndKey(t *testing.T) {
	got := buildRemoteSSHOpt(&RemoteDest{Host: "h", User: "u", Path: "/x", Port: 2222, SSHKey: "/root/.ssh/k"})
	if !strings.Contains(got, "ssh -p 2222") {
		t.Errorf("expected custom port in ssh opt, got %q", got)
	}
	if !strings.Contains(got, "-i /root/.ssh/k") {
		t.Errorf("expected -i with the key path, got %q", got)
	}
}

// TestRunBackup_Remote_RunsRsyncOverSSHWithExpectedArgs 驗證設了 Remote 的 Job
// 會走異地鏡像那條路:一次 rsync 呼叫,帶 -aAX --delete -e "ssh …",來源有尾斜線,
// 目的地是 user@host:path/(含尾斜線),而且完全不碰本機快照目錄。
func TestRunBackup_Remote_RunsRsyncOverSSHWithExpectedArgs(t *testing.T) {
	job := Job{
		ID:         "r1",
		Name:       "offsite",
		SourcePath: "/mnt/tank/media",
		Schedule:   Schedule{EveryHours: 24},
		Remote:     &RemoteDest{Host: "nas2", User: "backup", Port: 2222, Path: "/volume1/nas", SSHKey: "/root/.ssh/gonas"},
	}
	r := &recordingRunner{}

	result := RunBackup(context.Background(), r, job, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if result.SnapshotDir != "" {
		t.Errorf("remote mirror should not report a local snapshot dir, got %q", result.SnapshotDir)
	}

	if len(r.calls) != 1 || r.calls[0].name != "rsync" {
		t.Fatalf("expected exactly one rsync call, got %+v", r.calls)
	}
	args := r.calls[0].args
	if args[0] != "-aAX" || args[1] != "--delete" {
		t.Errorf("expected -aAX --delete first, got %v", args)
	}
	// -e 後面必須緊跟 ssh 指令字串。
	eIdx := -1
	for i, a := range args {
		if a == "-e" {
			eIdx = i
			break
		}
	}
	if eIdx < 0 || eIdx+1 >= len(args) {
		t.Fatalf("expected an -e <ssh opt> pair, got %v", args)
	}
	if !strings.Contains(args[eIdx+1], "ssh -p 2222") {
		t.Errorf("expected ssh opt after -e, got %q", args[eIdx+1])
	}
	src := args[len(args)-2]
	dst := args[len(args)-1]
	if src != "/mnt/tank/media/" {
		t.Errorf("expected source to have a trailing slash, got %q", src)
	}
	if dst != "backup@nas2:/volume1/nas/" {
		t.Errorf("expected dst user@host:path/ with trailing slash, got %q", dst)
	}
}

// TestRunBackup_Remote_RsyncFailureReturnsError 驗證遠端 rsync 失敗時整次備份標記失敗。
func TestRunBackup_Remote_RsyncFailureReturnsError(t *testing.T) {
	job := Job{
		ID:         "r2",
		Name:       "offsite",
		SourcePath: "/mnt/tank/media",
		Schedule:   Schedule{EveryHours: 24},
		Remote:     &RemoteDest{Host: "nas2", User: "backup", Path: "/volume1/nas"},
	}
	r := &recordingRunner{failErr: errors.New("ssh: connect to host nas2 port 22: Connection refused")}

	result := RunBackup(context.Background(), r, job, time.Now())
	if result.Success {
		t.Fatal("expected failure when remote rsync fails")
	}
	if result.Error == "" {
		t.Error("expected a non-empty error message on remote mirror failure")
	}
}

// TestRunBackup_Remote_InvalidRemoteFailsWithoutCallingRunner 驗證不合法的遠端設定
// (例如相對路徑)在 Validate 階段就被擋下,連 rsync 都不會被呼叫。
func TestRunBackup_Remote_InvalidRemoteFailsWithoutCallingRunner(t *testing.T) {
	job := Job{
		ID:         "r3",
		Name:       "offsite",
		SourcePath: "/mnt/tank/media",
		Schedule:   Schedule{EveryHours: 24},
		Remote:     &RemoteDest{Host: "nas2", User: "backup", Path: "relative/path"}, // 非絕對路徑
	}
	r := &recordingRunner{}

	result := RunBackup(context.Background(), r, job, time.Now())
	if result.Success {
		t.Fatal("expected failure for an invalid remote path")
	}
	if len(r.calls) != 0 {
		t.Errorf("expected rsync to never be called for an invalid job, got %+v", r.calls)
	}
}
