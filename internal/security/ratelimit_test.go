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
