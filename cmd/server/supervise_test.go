package main

import "testing"

// Tests never set GOPHER_MANAGED, so startBundledChildren must be completely
// inert: nil supervisor, nil error, and — crucially — it must NOT run the
// destructive legacy-layout migration or spawn caddy/rathole, even on a dev box
// that has embedded binaries AND a live legacy install. This guards against a
// test run stopping a real edge's services.
func TestStartBundledChildren_InertWithoutManagedEnv(t *testing.T) {
	t.Setenv("GOPHER_MANAGED", "") // explicitly not a managed edge

	sup, err := startBundledChildren()
	if err != nil {
		t.Fatalf("startBundledChildren err = %v, want nil", err)
	}
	if sup != nil {
		t.Fatal("returned a supervisor without GOPHER_MANAGED=1; must be inert in tests")
	}
}
