//go:build mattn

package sqlite

// Alternative driver for CGO builds: go build -tags mattn ./cmd/llmux
import _ "github.com/mattn/go-sqlite3"

const driverName = "sqlite3"

func dsn(path string) string {
	return "file:" + path + "?_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL"
}
