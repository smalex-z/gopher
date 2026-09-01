package service

import "testing"

// The sweep pushes on any byte difference (exactness is what keeps the file
// canonical) but only reports drift when the difference is substantive —
// a bootstrap-written file converging to merge formatting on the first sweep
// must not raise a "drift repaired" warning.
func TestConfigContentDiffers(t *testing.T) {
	canonical := `[client]
remote_addr = "router.example.com:2333"

# gopher-tunnel-start: t1
[client.services.tunnel-t1]
type = "tcp"
token = "tok"
local_addr = "localhost:3000"
# gopher-tunnel-end: t1
`
	cases := []struct {
		name  string
		other string
		want  bool
	}{
		{"identical", canonical, false},
		{"formatting only: extra blank lines and indentation", `[client]

remote_addr = "router.example.com:2333"


# gopher-tunnel-start: t1
  [client.services.tunnel-t1]
  type = "tcp"
  token = "tok"
  local_addr = "localhost:3000"
# gopher-tunnel-end: t1
`, false},
		{"tunnel section removed", `[client]
remote_addr = "router.example.com:2333"
`, true},
		{"token edited", `[client]
remote_addr = "router.example.com:2333"

# gopher-tunnel-start: t1
[client.services.tunnel-t1]
type = "tcp"
token = "TAMPERED"
local_addr = "localhost:3000"
# gopher-tunnel-end: t1
`, true},
		{"truncated mid-file", canonical[:len(canonical)/2], true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := configContentDiffers(canonical, tc.other); got != tc.want {
				t.Errorf("configContentDiffers = %v, want %v", got, tc.want)
			}
		})
	}
}
