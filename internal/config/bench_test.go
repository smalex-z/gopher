package config

import (
	"fmt"
	"testing"

	"github.com/smalex-z/gopher/internal/db"
)

func BenchmarkGenerateRatholeServerConfig_10(b *testing.B) {
	benchmarkGenerate(b, 5, 5)
}

func BenchmarkGenerateRatholeServerConfig_100(b *testing.B) {
	benchmarkGenerate(b, 50, 50)
}

func BenchmarkGenerateRatholeServerConfig_200(b *testing.B) {
	benchmarkGenerate(b, 100, 100)
}

func benchmarkGenerate(b *testing.B, nMachines, nTunnels int) {
	b.Helper()
	machines := make([]db.Machine, nMachines)
	for i := range machines {
		machines[i] = db.Machine{
			ID:              fmt.Sprintf("m%d", i),
			TunnelPort:      2000 + i,
			RatholeSSHToken: fmt.Sprintf("token-m%d", i),
		}
	}
	tunnels := make([]db.Tunnel, nTunnels)
	for i := range tunnels {
		tunnels[i] = db.Tunnel{
			ID:           fmt.Sprintf("t%d", i),
			RatholePort:  3000 + i,
			RatholeToken: fmt.Sprintf("token-t%d", i),
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GenerateRatholeServerConfig(machines, tunnels)
	}
}

func BenchmarkParseRatholeConfig_100Entries(b *testing.B) {
	machines := make([]db.Machine, 50)
	tunnels := make([]db.Tunnel, 50)
	for i := range machines {
		machines[i] = db.Machine{ID: fmt.Sprintf("m%d", i), TunnelPort: 2000 + i, RatholeSSHToken: fmt.Sprintf("tok%d", i)}
	}
	for i := range tunnels {
		tunnels[i] = db.Tunnel{ID: fmt.Sprintf("t%d", i), RatholePort: 3000 + i, RatholeToken: fmt.Sprintf("tok%d", i)}
	}
	cfg := GenerateRatholeServerConfig(machines, tunnels)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseRatholeConfig(cfg)
	}
}

func BenchmarkValidateSubdomain(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ValidateSubdomain("my-app-subdomain")
	}
}
