// Package logging provides human-facing log handlers.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	reset  = "\x1b[0m"
	dim    = "\x1b[2m"
	gray   = "\x1b[90m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	blue   = "\x1b[34m"
	cyan   = "\x1b[36m"
	bold   = "\x1b[1m"
)

// ColorHandler writes compact ANSI-colored slog records for local operation.
type ColorHandler struct {
	out    io.Writer
	level  slog.Leveler
	mu     *sync.Mutex
	attrs  []slog.Attr
	groups []string
}

// NewColorHandler returns a colorful, human-readable slog handler.
func NewColorHandler(out io.Writer, level slog.Leveler) *ColorHandler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &ColorHandler{out: out, level: level, mu: &sync.Mutex{}}
}

func (h *ColorHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *ColorHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]slog.Attr, 0, len(h.attrs)+r.NumAttrs())
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})

	var b strings.Builder
	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	b.WriteString(gray)
	b.WriteString(ts.Format("15:04:05"))
	b.WriteString(reset)
	b.WriteByte(' ')
	b.WriteString(levelColor(r.Level))
	b.WriteString(fmt.Sprintf("%-5s", r.Level.String()))
	b.WriteString(reset)
	b.WriteByte(' ')
	b.WriteString(messageColor(r.Level))
	b.WriteString(r.Message)
	b.WriteString(reset)

	for _, attr := range attrs {
		attr.Value = attr.Value.Resolve()
		if attr.Equal(slog.Attr{}) {
			continue
		}
		key := attr.Key
		if len(h.groups) > 0 {
			key = strings.Join(append(append([]string{}, h.groups...), key), ".")
		}
		b.WriteByte(' ')
		b.WriteString(dim)
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(reset)
		b.WriteString(attrValue(attr.Value))
	}
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func (h *ColorHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := h.clone()
	next.attrs = append(next.attrs, attrs...)
	return next
}

func (h *ColorHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := h.clone()
	next.groups = append(next.groups, name)
	return next
}

func (h *ColorHandler) clone() *ColorHandler {
	next := *h
	next.attrs = append([]slog.Attr{}, h.attrs...)
	next.groups = append([]string{}, h.groups...)
	return &next
}

func levelColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return bold + red
	case level >= slog.LevelWarn:
		return bold + yellow
	case level <= slog.LevelDebug:
		return cyan
	default:
		return bold + green
	}
}

func messageColor(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return red
	case level >= slog.LevelWarn:
		return yellow
	default:
		return reset
	}
}

func attrValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return colorString(v.String())
	case slog.KindInt64:
		return blue + fmt.Sprint(v.Int64()) + reset
	case slog.KindUint64:
		return blue + fmt.Sprint(v.Uint64()) + reset
	case slog.KindFloat64:
		return blue + fmt.Sprint(v.Float64()) + reset
	case slog.KindBool:
		return cyan + fmt.Sprint(v.Bool()) + reset
	case slog.KindDuration:
		return blue + v.Duration().String() + reset
	case slog.KindTime:
		return cyan + v.Time().Format(time.RFC3339) + reset
	case slog.KindAny:
		return colorString(fmt.Sprint(v.Any()))
	case slog.KindGroup:
		var parts []string
		for _, a := range v.Group() {
			parts = append(parts, a.Key+"="+attrValue(a.Value.Resolve()))
		}
		return "{" + strings.Join(parts, " ") + "}"
	default:
		return colorString(v.String())
	}
}

func colorString(s string) string {
	switch {
	case strings.HasPrefix(s, "2"):
		return green + s + reset
	case strings.HasPrefix(s, "4"):
		return yellow + s + reset
	case strings.HasPrefix(s, "5"):
		return red + s + reset
	case strings.Contains(strings.ToLower(s), "error"):
		return red + quoteIfNeeded(s) + reset
	default:
		return quoteIfNeeded(s)
	}
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\r\"") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
