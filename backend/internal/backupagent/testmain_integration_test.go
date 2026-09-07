//go:build integration

package backupagent

import (
	"os"
	"testing"

	"foldex/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.StopShared()
	os.Exit(code)
}
