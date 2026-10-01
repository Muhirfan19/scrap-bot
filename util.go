package main

import (
	"fmt"
	"time"
)

// logf prints a timestamped log line to stdout. Kept intentionally simple
// (no external logging library) so the bot has zero third-party
// dependencies and builds as a single static binary.
func logf(format string, args ...interface{}) {
	ts := time.Now().Format("15:04:05")
	fmt.Printf("%s | %s\n", ts, fmt.Sprintf(format, args...))
}
