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
