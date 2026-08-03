package ports

import "testing"

func TestInterfacesExist(t *testing.T) {
	// Compile-time assertion that interface shapes stay usable.
	var _ Uploader
	var _ Enqueuer
}
