// Ad-hoc DNS preflight tester. Not built into the main binary; run with:
//   go run ./cmd/dnsprobe <domain> [expected_ip]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/smalex-z/gopher/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dnsprobe <domain> [expected_ip]")
		os.Exit(2)
	}
	domain := os.Args[1]
	expectedIP := ""
	if len(os.Args) > 2 {
		expectedIP = os.Args[2]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result := service.RunDNSPreflight(ctx, domain, expectedIP)
	b, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(b))
}
