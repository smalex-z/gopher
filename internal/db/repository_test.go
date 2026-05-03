package db

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func initTestDB(t *testing.T) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	if err := Initialize(dsn); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
}

// seedMachine creates a minimal machine for tests that need a valid FK target.
func seedMachine(t *testing.T, id string) {
	t.Helper()
	_ = CreateMachine(&Machine{ID: id, Name: id, Status: "active"})
}

// seedTunnel creates a tunnel under machineID with given rathole port.
func seedTunnel(t *testing.T, id, machineID string, port int) {
	t.Helper()
	if err := CreateTunnel(&Tunnel{ID: id, MachineID: machineID, RatholePort: port}); err != nil {
		t.Fatalf("seedTunnel %s: %v", id, err)
	}
}

// ---- Machine CRUD -----------------------------------------------------------

func TestMachine_CreateAndGet(t *testing.T) {
	initTestDB(t)
	m := &Machine{
		ID:     "m1",
		Name:   "test-machine",
		Status: "connected",
	}
	if err := CreateMachine(m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	got, err := GetMachine("m1")
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if got.Name != "test-machine" {
		t.Errorf("Name = %q, want %q", got.Name, "test-machine")
	}
}

func TestMachine_GetNotFound(t *testing.T) {
	initTestDB(t)
	_, err := GetMachine("nonexistent")
	if err == nil {
		t.Fatal("expected error for missing machine, got nil")
	}
}

func TestMachine_Update(t *testing.T) {
	initTestDB(t)
	m := &Machine{ID: "m1", Name: "old", Status: "active"}
	if err := CreateMachine(m); err != nil {
		t.Fatalf("CreateMachine: %v", err)
	}
	m.Name = "new"
	if err := UpdateMachine(m); err != nil {
		t.Fatalf("UpdateMachine: %v", err)
	}
	got, _ := GetMachine("m1")
	if got.Name != "new" {
		t.Errorf("Name = %q, want %q", got.Name, "new")
	}
}

func TestMachine_Delete(t *testing.T) {
	initTestDB(t)
	m := &Machine{ID: "m1", Name: "del", Status: "active"}
	_ = CreateMachine(m)
	if err := DeleteMachine("m1"); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	_, err := GetMachine("m1")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestGetMachines_ReturnsAll(t *testing.T) {
	initTestDB(t)
	for i := 0; i < 3; i++ {
		_ = CreateMachine(&Machine{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("m%d", i), Status: "active"})
	}
	machines, err := GetMachines()
	if err != nil {
		t.Fatalf("GetMachines: %v", err)
	}
	if len(machines) != 3 {
		t.Errorf("len = %d, want 3", len(machines))
	}
}

// ---- Tunnel CRUD ------------------------------------------------------------

func TestTunnel_CreateAndGet(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	tun := &Tunnel{
		ID:          "t1",
		MachineID:   "m1",
		Name:        "web",
		Subdomain:   "web",
		RatholePort: 8080,
	}
	if err := CreateTunnel(tun); err != nil {
		t.Fatalf("CreateTunnel: %v", err)
	}
	got, err := GetTunnel("t1")
	if err != nil {
		t.Fatalf("GetTunnel: %v", err)
	}
	if got.Name != "web" {
		t.Errorf("Name = %q, want %q", got.Name, "web")
	}
}

func TestTunnel_GetNotFound(t *testing.T) {
	initTestDB(t)
	_, err := GetTunnel("nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTunnel_Delete(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	_ = CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", Name: "del", RatholePort: 9000})
	if err := DeleteTunnel("t1"); err != nil {
		t.Fatalf("DeleteTunnel: %v", err)
	}
	_, err := GetTunnel("t1")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestGetTunnelsByMachine(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	seedMachine(t, "m2")
	_ = CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", RatholePort: 8001})
	_ = CreateTunnel(&Tunnel{ID: "t2", MachineID: "m1", RatholePort: 8002})
	_ = CreateTunnel(&Tunnel{ID: "t3", MachineID: "m2", RatholePort: 8003})
	tunnels, err := GetTunnelsByMachine("m1")
	if err != nil {
		t.Fatalf("GetTunnelsByMachine: %v", err)
	}
	if len(tunnels) != 2 {
		t.Errorf("len = %d, want 2", len(tunnels))
	}
}

// ---- Port assignment --------------------------------------------------------

func TestNextRatholePort_Empty(t *testing.T) {
	initTestDB(t)
	port, err := NextRatholePort()
	if err != nil {
		t.Fatalf("NextRatholePort: %v", err)
	}
	if port != 1024 {
		t.Errorf("port = %d, want 1024", port)
	}
}

func TestNextRatholePort_SkipsUsed(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	// Use 1024 and 1025 in service tunnels
	seedTunnel(t, "t1", "m1", 1024)
	seedTunnel(t, "t2", "m1", 1025)
	port, err := NextRatholePort()
	if err != nil {
		t.Fatalf("NextRatholePort: %v", err)
	}
	if port != 1026 {
		t.Errorf("port = %d, want 1026", port)
	}
}

func TestNextRatholePort_SkipsMachinePorts(t *testing.T) {
	initTestDB(t)
	// Machine SSH tunnel occupies 1024
	_ = CreateMachine(&Machine{ID: "m1", TunnelPort: 1024})
	port, err := NextRatholePort()
	if err != nil {
		t.Fatalf("NextRatholePort: %v", err)
	}
	if port != 1025 {
		t.Errorf("port = %d, want 1025 (skipping machine port 1024), got %d", port, port)
	}
}

func TestNextRatholePort_FindsGap(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	// 1024, 1025 used; 1026 free; 1027 used
	seedTunnel(t, "t1", "m1", 1024)
	seedTunnel(t, "t2", "m1", 1025)
	seedTunnel(t, "t3", "m1", 1027)
	port, err := NextRatholePort()
	if err != nil {
		t.Fatalf("NextRatholePort: %v", err)
	}
	if port != 1026 {
		t.Errorf("port = %d, want 1026 (first gap)", port)
	}
}

func TestNextSSHTunnelPort_SharesPortSpace(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	seedTunnel(t, "t1", "m1", 1024)
	port, err := NextSSHTunnelPort()
	if err != nil {
		t.Fatalf("NextSSHTunnelPort: %v", err)
	}
	if port != 1025 {
		t.Errorf("port = %d, want 1025", port)
	}
}

// ---- Subdomain + port conflict checks ---------------------------------------

func TestCheckSubdomainExists(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	_ = CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", Subdomain: "myapp", RatholePort: 8080})

	exists, err := CheckSubdomainExists("myapp")
	if err != nil {
		t.Fatalf("CheckSubdomainExists: %v", err)
	}
	if !exists {
		t.Error("expected subdomain 'myapp' to exist")
	}

	exists, err = CheckSubdomainExists("other")
	if err != nil {
		t.Fatalf("CheckSubdomainExists: %v", err)
	}
	if exists {
		t.Error("expected subdomain 'other' to not exist")
	}
}

func TestCheckRatholePortExists_TunnelPort(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	_ = CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", RatholePort: 9999})

	exists, err := CheckRatholePortExists(9999)
	if err != nil {
		t.Fatalf("CheckRatholePortExists: %v", err)
	}
	if !exists {
		t.Error("expected port 9999 to exist in tunnels")
	}

	exists, err = CheckRatholePortExists(9998)
	if err != nil {
		t.Fatalf("CheckRatholePortExists: %v", err)
	}
	if exists {
		t.Error("expected port 9998 to not exist")
	}
}

func TestCheckRatholePortExists_MachinePort(t *testing.T) {
	initTestDB(t)
	_ = CreateMachine(&Machine{ID: "m1", TunnelPort: 2222})

	exists, err := CheckRatholePortExists(2222)
	if err != nil {
		t.Fatalf("CheckRatholePortExists (machine): %v", err)
	}
	if !exists {
		t.Error("expected port 2222 to exist via machine tunnel_port")
	}
}

// ---- AppSettings ------------------------------------------------------------

func TestSettings_DefaultWhenAbsent(t *testing.T) {
	initTestDB(t)
	s, err := GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.IsSetup {
		t.Error("expected IsSetup=false for fresh DB")
	}
}

func TestSettings_SaveAndReload(t *testing.T) {
	initTestDB(t)
	s := &AppSettings{
		ID:             "singleton",
		IsSetup:        true,
		Domain:         "example.com",
		UpdateChannel:  "beta",
		LocalSetupDone: true,
	}
	if err := SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if !got.IsSetup {
		t.Error("IsSetup should be true")
	}
	if got.Domain != "example.com" {
		t.Errorf("Domain = %q, want %q", got.Domain, "example.com")
	}
	if got.UpdateChannel != "beta" {
		t.Errorf("UpdateChannel = %q, want %q", got.UpdateChannel, "beta")
	}
}

func TestSettings_Update(t *testing.T) {
	initTestDB(t)
	_ = SaveSettings(&AppSettings{ID: "singleton", Domain: "old.com"})
	_ = SaveSettings(&AppSettings{ID: "singleton", Domain: "new.com"})
	got, _ := GetSettings()
	if got.Domain != "new.com" {
		t.Errorf("Domain = %q, want %q", got.Domain, "new.com")
	}
}

// ---- BotSession -------------------------------------------------------------

func TestBotSession_CreateAndPurge(t *testing.T) {
	initTestDB(t)
	now := time.Now()

	// Active session (expires in the future)
	_ = CreateBotSession(&BotSession{
		ID:        "sess-active",
		TunnelID:  "t1",
		IP:        "1.2.3.4",
		IssuedAt:  now,
		ExpiresAt: now.Add(time.Hour),
	})
	// Expired session
	_ = CreateBotSession(&BotSession{
		ID:        "sess-expired",
		TunnelID:  "t1",
		IP:        "1.2.3.5",
		IssuedAt:  now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour),
	})

	if err := PurgeBotSessions(); err != nil {
		t.Fatalf("PurgeBotSessions: %v", err)
	}

	var count int64
	DB.Model(&BotSession{}).Count(&count)
	if count != 1 {
		t.Errorf("after purge: count = %d, want 1 (active session only)", count)
	}
}

// ---- Bootstrap token --------------------------------------------------------

func TestBootstrapToken_CreateAndGet(t *testing.T) {
	initTestDB(t)
	exp := time.Now().Add(time.Hour)
	bt := &BootstrapToken{
		ID:        "bt1",
		Token:     "secret-token",
		ExpiresAt: exp,
	}
	if err := CreateBootstrapToken(bt); err != nil {
		t.Fatalf("CreateBootstrapToken: %v", err)
	}
	got, err := GetBootstrapToken("secret-token")
	if err != nil {
		t.Fatalf("GetBootstrapToken: %v", err)
	}
	if got.ID != "bt1" {
		t.Errorf("ID = %q, want %q", got.ID, "bt1")
	}
}

func TestBootstrapToken_NotFound(t *testing.T) {
	initTestDB(t)
	_, err := GetBootstrapToken("nope")
	if err == nil {
		t.Fatal("expected error for missing token, got nil")
	}
}

func TestBootstrapToken_MarkUsed(t *testing.T) {
	initTestDB(t)
	_ = CreateBootstrapToken(&BootstrapToken{
		ID:        "bt1",
		Token:     "tok",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	machineID := "m1"
	if err := MarkTokenUsed("bt1", machineID); err != nil {
		t.Fatalf("MarkTokenUsed: %v", err)
	}
	got, _ := GetBootstrapToken("tok")
	if got.MachineID == nil || *got.MachineID != machineID {
		t.Errorf("MachineID = %v, want %q", got.MachineID, machineID)
	}
	if got.UsedAt == nil {
		t.Error("UsedAt should be set after marking used")
	}
}

// ---- GetTunnelBySubdomain ---------------------------------------------------

func TestGetTunnelBySubdomain(t *testing.T) {
	initTestDB(t)
	seedMachine(t, "m1")
	_ = CreateTunnel(&Tunnel{ID: "t1", MachineID: "m1", Subdomain: "api", RatholePort: 8000})

	got, err := GetTunnelBySubdomain("api")
	if err != nil {
		t.Fatalf("GetTunnelBySubdomain: %v", err)
	}
	if got.ID != "t1" {
		t.Errorf("ID = %q, want %q", got.ID, "t1")
	}

	_, err = GetTunnelBySubdomain("missing")
	if err == nil {
		t.Fatal("expected error for missing subdomain, got nil")
	}
}

// ---- Partial machine updaters -----------------------------------------------
//
// SetMachineStatus and SetMachineAgentSeen both exist so concurrent writers
// (monitor + health) don't clobber each other's columns via a full-record
// GORM Save. These tests lock the contract: each helper updates *only* its
// declared columns and leaves the rest alone.

func TestSetMachineStatus_OnlyUpdatesStatusAndLastSeen(t *testing.T) {
	initTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	m := &Machine{
		ID:                "m1",
		Name:              "preserve-me",
		Status:            "pending",
		AgentInstalled:    true,
		AgentVersion:      "0.1.0",
		AgentLastSeen:     &now,
		AgentToken:        "secret",
		AgentRemotePort:   1234,
		AgentLocalPort:    4322,
		AgentRatholeToken: "rt",
	}
	if err := CreateMachine(m); err != nil {
		t.Fatalf("create: %v", err)
	}

	later := now.Add(2 * time.Minute)
	if err := SetMachineStatus("m1", "offline", &later); err != nil {
		t.Fatalf("SetMachineStatus: %v", err)
	}

	got, err := GetMachine("m1")
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if got.Status != "offline" {
		t.Errorf("Status = %q, want offline", got.Status)
	}
	if got.LastSeen == nil || !got.LastSeen.Equal(later) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, later)
	}
	// Untouched fields stay put.
	if !got.AgentInstalled {
		t.Errorf("AgentInstalled was clobbered")
	}
	if got.AgentVersion != "0.1.0" {
		t.Errorf("AgentVersion = %q, want 0.1.0", got.AgentVersion)
	}
	if got.AgentToken != "secret" {
		t.Errorf("AgentToken was clobbered")
	}
	if got.AgentRemotePort != 1234 {
		t.Errorf("AgentRemotePort was clobbered")
	}
}

func TestSetMachineAgentSeen_FlipsInstalledAndPreservesNonAgentFields(t *testing.T) {
	initTestDB(t)
	m := &Machine{
		ID:                 "m2",
		Name:               "agent-seen",
		Status:             "pending",
		TunnelPort:         1024,
		RatholeSSHToken:    "ssh-tok",
		AgentInstalled:     false,
		AgentRemotePort:    1025,
		AgentLocalPort:     4322,
		AgentRatholeToken:  "agent-rt",
		AgentInstallError:  "previous error",
	}
	if err := CreateMachine(m); err != nil {
		t.Fatalf("create: %v", err)
	}

	when := time.Now().UTC().Truncate(time.Second)
	if err := SetMachineAgentSeen("m2", "0.1.0", when); err != nil {
		t.Fatalf("SetMachineAgentSeen: %v", err)
	}

	got, err := GetMachine("m2")
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if !got.AgentInstalled {
		t.Errorf("AgentInstalled should flip true once agent is reachable")
	}
	if got.AgentVersion != "0.1.0" {
		t.Errorf("AgentVersion = %q, want 0.1.0", got.AgentVersion)
	}
	if got.AgentInstallError != "" {
		t.Errorf("AgentInstallError should clear on successful sighting, got %q", got.AgentInstallError)
	}
	if got.Status != "connected" {
		t.Errorf("Status should reflect successful agent contact, got %q", got.Status)
	}
	if got.AgentLastSeen == nil || !got.AgentLastSeen.Equal(when) {
		t.Errorf("AgentLastSeen = %v, want %v", got.AgentLastSeen, when)
	}
	// Non-agent fields preserved.
	if got.Name != "agent-seen" {
		t.Errorf("Name was clobbered")
	}
	if got.TunnelPort != 1024 {
		t.Errorf("TunnelPort was clobbered")
	}
	if got.RatholeSSHToken != "ssh-tok" {
		t.Errorf("RatholeSSHToken was clobbered")
	}
}
