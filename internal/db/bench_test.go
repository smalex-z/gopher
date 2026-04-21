package db

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkNextRatholePort_Empty(b *testing.B) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll("BenchmarkNextRatholePort_Empty", "/", "_"))
	if err := Initialize(dsn); err != nil {
		b.Fatalf("Initialize: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = NextRatholePort()
	}
}

func BenchmarkNextRatholePort_100Entries(b *testing.B) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll("BenchmarkNextRatholePort_100Entries", "/", "_"))
	if err := Initialize(dsn); err != nil {
		b.Fatalf("Initialize: %v", err)
	}
	// Pre-populate 100 tunnels and machines filling ports 1024..1123
	_ = CreateMachine(&Machine{ID: "bm0", TunnelPort: 0})
	for i := 0; i < 50; i++ {
		_ = CreateMachine(&Machine{ID: fmt.Sprintf("bm%d", i+1), TunnelPort: 1024 + i})
	}
	for i := 0; i < 50; i++ {
		_ = CreateTunnel(&Tunnel{ID: fmt.Sprintf("bt%d", i), MachineID: "bm0", RatholePort: 1074 + i})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = NextRatholePort()
	}
}

func BenchmarkCheckSubdomainExists(b *testing.B) {
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll("BenchmarkCheckSubdomainExists", "/", "_"))
	if err := Initialize(dsn); err != nil {
		b.Fatalf("Initialize: %v", err)
	}
	_ = CreateMachine(&Machine{ID: "bm0"})
	for i := 0; i < 50; i++ {
		_ = CreateTunnel(&Tunnel{
			ID:        fmt.Sprintf("bt%d", i),
			MachineID: "bm0",
			Subdomain: fmt.Sprintf("sub%d", i),
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = CheckSubdomainExists("sub25")
	}
}
