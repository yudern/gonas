package security

import (
	"testing"
	"time"
)

func TestLoginLimiter_AllowsUntilThreshold(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)

	for i := 0; i < 2; i++ {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("expected attempt %d to be allowed before hitting the threshold", i+1)
		}
		l.RecordFailure("1.2.3.4")
	}

	// 第三次失敗前應該還是允許嘗試(還沒到門檻)。
	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Fatal("expected the attempt right before hitting the threshold to still be allowed")
	}
	l.RecordFailure("1.2.3.4")

	ok, wait := l.Allow("1.2.3.4")
	if ok {
		t.Fatal("expected key to be locked out after reaching maxFailures")
	}
	if wait <= 0 {
		t.Errorf("expected a positive wait duration, got %v", wait)
	}
}

func TestLoginLimiter_LockoutExpires(t *testing.T) {
	l := NewLoginLimiter(1, 20*time.Millisecond)

	l.RecordFailure("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Fatal("expected key to be locked out immediately after reaching maxFailures")
	}

	time.Sleep(40 * time.Millisecond)

	if ok, wait := l.Allow("1.2.3.4"); !ok {
		t.Errorf("expected lockout to have expired, still locked with wait=%v", wait)
	}
}

func TestLoginLimiter_SuccessResetsFailures(t *testing.T) {
	l := NewLoginLimiter(3, time.Minute)

	l.RecordFailure("1.2.3.4")
	l.RecordFailure("1.2.3.4")
	l.RecordSuccess("1.2.3.4")

	// 成功之後失敗計數應該歸零,重新累積兩次失敗不該直接觸發鎖定。
	l.RecordFailure("1.2.3.4")
	l.RecordFailure("1.2.3.4")

	if ok, _ := l.Allow("1.2.3.4"); !ok {
		t.Fatal("expected failures to have been reset by RecordSuccess")
	}
}

func TestLoginLimiter_KeysAreIndependent(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)

	l.RecordFailure("1.2.3.4")
	if ok, _ := l.Allow("1.2.3.4"); ok {
		t.Fatal("expected 1.2.3.4 to be locked out")
	}
	if ok, _ := l.Allow("5.6.7.8"); !ok {
		t.Fatal("expected a different key to be unaffected by another key's lockout")
	}
}

func TestLoginLimiter_AllowDoesNotMutateState(t *testing.T) {
	l := NewLoginLimiter(1, time.Minute)

	// 連續呼叫 Allow 不應該自己把狀態變成鎖定 —— 只有 RecordFailure 才會。
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("1.2.3.4"); !ok {
			t.Fatalf("call %d: Allow should not lock out a key on its own", i)
		}
	}
}

// 第三十輪覆核回歸:未達鎖定門檻、閒置超過一個鎖定週期的項目要被清掉,
// 不能讓「每個來源送一次失敗」把 map 無上限撐大。
func TestLoginLimiter_SweepsStaleSubThresholdEntries(t *testing.T) {
	l := NewLoginLimiter(5, 10*time.Millisecond)
	l.RecordFailure("stale") // 只失敗一次,不會鎖定
	if len(l.attempts) != 1 {
		t.Fatalf("expected 1 tracked entry, got %d", len(l.attempts))
	}
	time.Sleep(20 * time.Millisecond)
	l.RecordFailure("fresh") // 觸發清理,stale 應被掃掉
	if _, ok := l.attempts["stale"]; ok {
		t.Error("stale sub-threshold entry should have been swept")
	}
	if _, ok := l.attempts["fresh"]; !ok {
		t.Error("fresh entry should still be tracked")
	}
}
