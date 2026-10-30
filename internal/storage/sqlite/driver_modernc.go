//go:build !mattn

package sqlite

// Default driver: modernc.org/sqlite is pure Go, so LLMux stays a static,
// CGO-free binary that cross-compiles everywhere (including Windows without gcc).
import _ "modernc.org/sqlite"

const driverName = "sqlite"

func dsn(path string) string {
	return path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}
