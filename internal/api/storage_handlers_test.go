package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// smartTestRunner 假裝有兩顆硬碟(lsblk)，其中一顆能正常查到 SMART 資料、
// 另一顆查詢會失敗(模擬「這台主機沒裝 smartctl」或「裝置不支援 SMART」
// 這種常見情況)——用來驗證 handleStorageDisksSmart 對「單顆碟查詢失敗」
// 的處理是把錯誤放進該筆結果的 Error 欄位、其餘硬碟正常回傳，而不是讓
// 整支 API 因為一顆碟查不到就跟著回錯。
type smartTestRunner struct{}

const lsblkTwoDisksJSON = `{"blockdevices":[
	{"name":"sda","path":"/dev/sda","type":"disk","size":"1000000000000","model":"Healthy Disk","serial":"S1","fstype":"","mountpoint":"","rota":true},
	{"name":"sdb","path":"/dev/sdb","type":"disk","size":"1000000000000","model":"Unsupported Disk","serial":"S2","fstype":"","mountpoint":"","rota":true}
]}`

func (smartTestRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	switch name {
	case "lsblk":
		return []byte(lsblkTwoDisksJSON), nil
	case "smartctl":
		// args 是 ["-a", device]；用最後一個參數判斷是哪一顆碟。
		device := args[len(args)-1]
		if device == "/dev/sdb" {
			return nil, errors.New("exec: \"smartctl\": executable file not found in $PATH")
		}
		return []byte(sampleSmartctlOutputForHandlerTest), nil
	default:
		return nil, errors.New("unexpected command: " + name)
	}
}

func (smartTestRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin call: " + name)
}

const sampleSmartctlOutputForHandlerTest = `smartctl 7.3 2022-02-28 r5338 [x86_64-linux-6.1.0] (local build)

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED

ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
194 Temperature_Celsius     0x0022   116   105   000    Old_age   Always       -       31
`

func TestHandleStorageDisksSmart_MixedSuccessAndFailure(t *testing.T) {
	s := newTestServer(t)
	s.runner = smartTestRunner{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/storage/disks/smart", nil)
	rec := httptest.NewRecorder()
	s.handleStorageDisksSmart(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var results []diskSmartResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(results), results)
	}

	sda, sdb := results[0], results[1]
	if sda.Path != "/dev/sda" {
		t.Errorf("expected first result for /dev/sda, got %q", sda.Path)
	}
	if !sda.Passed {
		t.Error("expected /dev/sda to report Passed=true")
	}
	if sda.TempCelsius == nil || *sda.TempCelsius != 31 {
		t.Errorf("expected /dev/sda temp 31, got %v", sda.TempCelsius)
	}
	if sda.Error != "" {
		t.Errorf("expected no error for /dev/sda, got %q", sda.Error)
	}

	if sdb.Path != "/dev/sdb" {
		t.Errorf("expected second result for /dev/sdb, got %q", sdb.Path)
	}
	if sdb.Error == "" {
		t.Error("expected an error for /dev/sdb (simulated missing smartctl), got none")
	}
	if !strings.Contains(sdb.Error, "smartctl") {
		t.Errorf("expected error to mention smartctl, got %q", sdb.Error)
	}
	if sdb.TempCelsius != nil {
		t.Errorf("expected no temp reading for a failed lookup, got %v", sdb.TempCelsius)
	}
}

// lsblkFailureRunner 模擬 lsblk 本身就失敗的情況(例如指令不存在)——這時
// 整支 API 應該回 500,因為連「有哪些硬碟」都不知道，沒有辦法回傳任何
// 有意義的部分結果。
type lsblkFailureRunner struct{}

func (lsblkFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, errors.New("lsblk: command not found")
}
func (lsblkFailureRunner) RunWithStdin(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunWithStdin call")
}

func TestHandleStorageDisksSmart_DiskDiscoveryFailure(t *testing.T) {
	s := newTestServer(t)
	s.runner = lsblkFailureRunner{}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/storage/disks/smart", nil)
	rec := httptest.NewRecorder()
	s.handleStorageDisksSmart(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when disk discovery itself fails, got %d: %s", rec.Code, rec.Body.String())
	}
}
