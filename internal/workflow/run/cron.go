package run

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronExpr struct {
	minute  cronField
	hour    cronField
	day     cronField
	month   cronField
	weekday cronField
	// dayStar/weekdayStar are true when the original field was a bare "*",
	// used to apply Vixie cron's OR-of-day-and-weekday rule when BOTH are
	// restricted.
	dayStar     bool
	weekdayStar bool
}

type cronField map[int]bool

func parseCron(expr string) (cronExpr, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return cronExpr{}, fmt.Errorf("expected 5 fields")
	}
	minute, err := parseCronField(parts[0], 0, 59)
	if err != nil {
		return cronExpr{}, fmt.Errorf("minute: %w", err)
	}
	hour, err := parseCronField(parts[1], 0, 23)
	if err != nil {
		return cronExpr{}, fmt.Errorf("hour: %w", err)
	}
	day, err := parseCronField(parts[2], 1, 31)
	if err != nil {
		return cronExpr{}, fmt.Errorf("day: %w", err)
	}
	month, err := parseCronField(parts[3], 1, 12)
	if err != nil {
		return cronExpr{}, fmt.Errorf("month: %w", err)
	}
	weekday, err := parseCronField(parts[4], 0, 7)
	if err != nil {
		return cronExpr{}, fmt.Errorf("weekday: %w", err)
	}
	if weekday[7] {
		weekday[0] = true
		delete(weekday, 7)
	}
	return cronExpr{
		minute: minute, hour: hour, day: day, month: month, weekday: weekday,
		dayStar:     parts[2] == "*",
		weekdayStar: parts[4] == "*",
	}, nil
}

func parseCronField(field string, min, max int) (cronField, error) {
	out := cronField{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty item")
		}
		step := 1
		if strings.Contains(part, "/") {
			pair := strings.Split(part, "/")
			if len(pair) != 2 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			part = pair[0]
			n, err := strconv.Atoi(pair[1])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("bad step %q", pair[1])
			}
			step = n
		}
		start, end := min, max
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			pair := strings.Split(part, "-")
			if len(pair) != 2 {
				return nil, fmt.Errorf("bad range %q", part)
			}
			var err error
			start, err = strconv.Atoi(pair[0])
			if err != nil {
				return nil, err
			}
			end, err = strconv.Atoi(pair[1])
			if err != nil {
				return nil, err
			}
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, err
			}
			start, end = n, n
		}
		if start < min || end > max || start > end {
			return nil, fmt.Errorf("value out of range %d-%d", min, max)
		}
		for i := start; i <= end; i += step {
			out[i] = true
		}
	}
	return out, nil
}

func (c cronExpr) matches(t time.Time) bool {
	t = t.UTC()
	dayMatch := c.day[t.Day()]
	weekdayMatch := c.weekday[int(t.Weekday())]
	// Vixie cron: when BOTH day-of-month and day-of-week are restricted (not
	// bare "*"), the match is their OR; otherwise it is their AND (the "*"
	// side is all-true, so AND still reduces to the restricted side).
	dayOK := dayMatch && weekdayMatch
	if !c.dayStar && !c.weekdayStar {
		dayOK = dayMatch || weekdayMatch
	}
	return c.minute[t.Minute()] &&
		c.hour[t.Hour()] &&
		dayOK &&
		c.month[int(t.Month())]
}
