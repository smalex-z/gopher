package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
)

// startTestAgent runs the real agentServer (with the auth interceptors) on a
// loopback listener and returns its address.
func startTestAgent(t *testing.T, token string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(unaryAuthInterceptor(token)),
		grpc.StreamInterceptor(streamAuthInterceptor(token)),
	)
	agentpb.RegisterAgentControlServer(srv, &agentServer{
		cfg:       config{Token: token, UnitName: "rathole-client.service"},
		startedAt: time.Now(),
	})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// dialAgent connects with the given token (empty = no credentials attached).
func dialAgent(t *testing.T, addr, token string) agentpb.AgentControlClient {
	t.Helper()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(bearerToken{token: token}))
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentpb.NewAgentControlClient(conn)
}

// bearerToken mirrors the client-side PerRPCCredentials so the test can attach
// (or omit) the token.
type bearerToken struct{ token string }

func (b bearerToken) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}
func (bearerToken) RequireTransportSecurity() bool { return false }

func TestGRPC_MissingTokenIsUnauthenticated(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "") // no creds
	_, err := client.GetVersion(context.Background(), &agentpb.GetVersionRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestGRPC_WrongTokenIsUnauthenticated(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "wrong")
	_, err := client.GetVersion(context.Background(), &agentpb.GetVersionRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestGRPC_CorrectTokenSucceeds(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	resp, err := client.GetVersion(context.Background(), &agentpb.GetVersionRequest{})
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if resp.GetVersion() != agentVersion {
		t.Errorf("version: got %q want %q", resp.GetVersion(), agentVersion)
	}
	if resp.GetProtocolVersion() != protocolVersion {
		t.Errorf("protocol_version: got %d want %d", resp.GetProtocolVersion(), protocolVersion)
	}
}

func TestGRPC_SetManagedKeyValidation(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	cases := []struct {
		name     string
		req      *agentpb.SetManagedKeyRequest
		wantCode codes.Code
	}{
		{"empty username", &agentpb.SetManagedKeyRequest{PublicKey: "ssh-rsa AAAA"}, codes.InvalidArgument},
		{"empty pubkey", &agentpb.SetManagedKeyRequest{Username: "ubuntu"}, codes.InvalidArgument},
		{"multiline pubkey", &agentpb.SetManagedKeyRequest{Username: "ubuntu", PublicKey: "ssh-rsa AAAA\nssh-rsa BBBB"}, codes.InvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.SetManagedKey(context.Background(), tc.req)
			if status.Code(err) != tc.wantCode {
				t.Fatalf("got %v, want %v", status.Code(err), tc.wantCode)
			}
		})
	}
}

func TestGRPC_PutRatholeConfigRejectsEmpty(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	_, err := client.PutRatholeConfig(context.Background(), &agentpb.RatholeConfig{Toml: ""})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestGRPC_PutRatholeConfigRejectsOversize(t *testing.T) {
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	big := strings.Repeat("a", maxRatholeConfigBytes+10)
	_, err := client.PutRatholeConfig(context.Background(), &agentpb.RatholeConfig{Toml: big})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestGRPC_UninstallMissingScript(t *testing.T) {
	// If /usr/local/bin/gopher-uninstall is present on the dev box (rare), the
	// RPC would spawn a real worker. Skip rather than risk that.
	if _, err := os.Stat("/usr/local/bin/gopher-uninstall"); err == nil {
		t.Skip("/usr/local/bin/gopher-uninstall present; skipping to avoid spawning a real uninstall")
	}
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	_, err := client.Uninstall(context.Background(), &agentpb.UninstallRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestGRPC_GetRatholeConfigMissingFile(t *testing.T) {
	if _, err := os.Stat(clientTomlPath); err == nil {
		t.Skip("real " + clientTomlPath + " present; cannot exercise missing-file branch")
	}
	addr := startTestAgent(t, "secret")
	client := dialAgent(t, addr, "secret")
	_, err := client.GetRatholeConfig(context.Background(), &agentpb.GetRatholeConfigRequest{})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}
