package run

import (
	"testing"
	"time"
)

func TestCronMatches(t *testing.T) {
	ts := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	ok, err := CronMatches("*/15 9 * * 1", ts)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected cron to match")
	}
	ok, err = CronMatches("0 9 * * 1", ts)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected cron not to match")
	}
}

func TestCronRejectsBadExpressions(t *testing.T) {
	if _, err := CronMatches("bad", time.Now()); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := CronMatches("61 * * * *", time.Now()); err == nil {
		t.Fatal("expected range error")
	}
}

// TestCronVixieDayWeekdayOR verifies the Vixie rule: when both day-of-month
// and day-of-week are restricted, a match on EITHER satisfies the day
// constraint. "0 0 13 * 5" fires on the 13th OR on a Friday at 00:00.
func TestCronVixieDayWeekdayOR(t *testing.T) {
	// 2026-08-13 is a Thursday; 2026-08-14 is a Friday.
	thirteenth := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC) // day=13, weekday=Thu(4)
	friday := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)    // day=14, weekday=Fri(5)
	both := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)      // day=15, weekday=Sat(6)

	// "0 0 13 * 5" — match on day==13 OR weekday==5(Fri).
	ok, err := CronMatches("0 0 13 * 5", thirteenth)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match on day=13 even though weekday is Thu")
	}
	ok, err = CronMatches("0 0 13 * 5", friday)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected match on weekday=Fri even though day is 14")
	}
	ok, _ = CronMatches("0 0 13 * 5", both)
	if ok {
		t.Error("expected no match when neither day=13 nor weekday=Fri")
	}

	// When day is a wildcard, only the restricted weekday matters (AND, which
	// reduces to the weekday because day is all-true): "0 0 * * 5" matches
	// Friday only.
	if ok, _ := CronMatches("0 0 * * 5", thirteenth); ok {
		t.Error("0 0 * * 5 should not match Thursday")
	}
	if ok, _ := CronMatches("0 0 * * 5", friday); !ok {
		t.Error("0 0 * * 5 should match Friday")
	}
}
