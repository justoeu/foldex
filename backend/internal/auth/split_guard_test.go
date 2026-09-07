package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ARCH-ATL-008: the god package splits along already-physical file
// boundaries. anomaly and ipblock are the first leaves — types, ranking,
// rails and the in-memory blocklist live in subpackages so they can grow
// without enlarging this namespace. The SQL that joins audit_log stays on
// Repository (it needs auth vocab); the leaf code must not.
func TestGodPackageSplit_AnomalyAndIPBlockAreSubpackages(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	dir := filepath.Dir(file)
	for _, name := range []string{"ipblock", "anomaly"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.IsDir() {
			t.Errorf("expected subpackage internal/auth/%s/", name)
		}
	}
	for _, name := range []string{"ipblock.go", "anomaly.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s still lives in the god package; it belongs in a subpackage", name)
		}
	}
}
