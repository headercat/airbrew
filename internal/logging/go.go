// Package logging — go.go
//
// logging.Go starts a goroutine whose panics are recovered and logged, so a
// single unexpected panic in a background worker (workflow engine, mail
// outbox sweeper, drive zip stream, AI runtime) cannot crash the whole
// process. The HTTP-handler path is already covered by middleware.Recover;
// this covers goroutines that escape the request lifecycle.
package logging

import (
	"log/slog"
	"runtime/debug"
)

// Go runs fn in a new goroutine, recovering and logging any panic. It is
// intended for fire-and-forget workers spawned by request handlers or
// background loops; goroutines that must surface an error to a caller should
// be structured with an explicit error channel instead.
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("background goroutine panicked",
					"worker", name, "panic", r, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}
