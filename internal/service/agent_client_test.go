package service

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentpb "github.com/smalex-z/gopher/internal/agentpb"
	"github.com/smalex-z/gopher/internal/db"
)

// fakeAgent is a configurable in-process AgentControl gRPC server. Each RPC the
// tests care about is backed by a closure; unset RPCs return Unimplemented.
type fakeAgent struct {
	agentpb.UnimplementedAgentControlServer
	getConfig func(context.Context, *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error)
	putConfig func(context.Context, *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error)
	uninstall func(context.Context, *agentpb.UninstallRequest) (*agentpb.UninstallResponse, error)
}

func (f *fakeAgent) GetRatholeConfig(ctx context.Context, in *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
	if f.getConfig == nil {
		return nil, status.Error(codes.Unimplemented, "getConfig")
	}
	return f.getConfig(ctx, in)
}

func (f *fakeAgent) PutRatholeConfig(ctx context.Context, in *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
	if f.putConfig == nil {
		return nil, status.Error(codes.Unimplemented, "putConfig")
	}
	return f.putConfig(ctx, in)
}

func (f *fakeAgent) Uninstall(ctx context.Context, in *agentpb.UninstallRequest) (*agentpb.UninstallResponse, error) {
	if f.uninstall == nil {
		return nil, status.Error(codes.Unimplemented, "uninstall")
	}
	return f.uninstall(ctx, in)
}

// startFakeAgent runs impl on a real loopback gRPC server and returns a machine
// whose AgentRemotePort points at it (mirroring the rathole back-channel the
// AgentClient dials in production).
func startFakeAgent(t *testing.T, impl agentpb.AgentControlServer) *db.Machine {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	agentpb.RegisterAgentControlServer(srv, impl)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	_, portStr, _ := net.SplitHostPort(lis.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return &db.Machine{
		ID:              "test",
		AgentInstalled:  true,
		AgentRemotePort: port,
		AgentToken:      "test-token",
	}
}

// authToken extracts the bearer token from incoming gRPC metadata.
func authToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimPrefix(vals[0], "Bearer ")
}

func TestAgentClient_GetRatholeConfig_Success(t *testing.T) {
	const body = "[client]\nremote_addr = \"router:2333\"\n"
	machine := startFakeAgent(t, &fakeAgent{
		getConfig: func(ctx context.Context, _ *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
			if got := authToken(ctx); got != "test-token" {
				t.Errorf("missing/wrong bearer token: %q", got)
			}
			return &agentpb.RatholeConfig{Toml: body}, nil
		},
	})

	got, err := NewAgentClient(machine).GetRatholeConfig(context.Background())
	if err != nil {
		t.Fatalf("GetRatholeConfig: %v", err)
	}
	if got != body {
		t.Fatalf("body mismatch: got %q want %q", got, body)
	}
}

func TestAgentClient_GetRatholeConfig_NonOK(t *testing.T) {
	machine := startFakeAgent(t, &fakeAgent{
		getConfig: func(context.Context, *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
			return nil, status.Error(codes.NotFound, "no client.toml")
		},
	})

	_, err := NewAgentClient(machine).GetRatholeConfig(context.Background())
	if err == nil {
		t.Fatal("expected error on NotFound")
	}
	if !strings.Contains(err.Error(), "no client.toml") {
		t.Errorf("error should surface server detail: %v", err)
	}
}

func TestAgentClient_PutRatholeConfig_SendsBodyAndAuth(t *testing.T) {
	const newConfig = "[client]\nremote_addr = \"router:2333\"\n[client.services.tunnel-x]\n"
	var seen string
	machine := startFakeAgent(t, &fakeAgent{
		putConfig: func(ctx context.Context, in *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
			if got := authToken(ctx); got != "test-token" {
				t.Errorf("missing/wrong bearer token: %q", got)
			}
			seen = in.GetToml()
			return &agentpb.PutRatholeConfigResponse{Written: true}, nil
		},
	})

	if err := NewAgentClient(machine).PutRatholeConfig(context.Background(), newConfig); err != nil {
		t.Fatalf("PutRatholeConfig: %v", err)
	}
	if seen != newConfig {
		t.Errorf("server got %q, want %q", seen, newConfig)
	}
}

func TestAgentClient_PutRatholeConfig_NonOK(t *testing.T) {
	machine := startFakeAgent(t, &fakeAgent{
		putConfig: func(context.Context, *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
			return nil, status.Error(codes.Internal, "disk full")
		},
	})

	err := NewAgentClient(machine).PutRatholeConfig(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on Internal")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error should surface server detail: %v", err)
	}
}

// updateClientTomlViaAgent runs the read-transform-write loop against a real
// gRPC test server, exercising the agent path end-to-end on the VPS side.
func TestUpdateClientTomlViaAgent_HappyPath(t *testing.T) {
	current := "[client]\nremote_addr = \"router:2333\"\n"
	expected := current + "[client.services.tunnel-new]\n"

	var got string
	machine := startFakeAgent(t, &fakeAgent{
		getConfig: func(context.Context, *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
			return &agentpb.RatholeConfig{Toml: current}, nil
		},
		putConfig: func(_ context.Context, in *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
			got = in.GetToml()
			return &agentpb.PutRatholeConfigResponse{Written: true}, nil
		},
	})

	s := &LocalSetupService{}
	err := s.updateClientTomlViaAgent(machine, func(existing string) (string, error) {
		if existing != current {
			t.Errorf("transform got existing=%q, want %q", existing, current)
		}
		return expected, nil
	})
	if err != nil {
		t.Fatalf("updateClientTomlViaAgent: %v", err)
	}
	if got != expected {
		t.Errorf("agent received %q, want %q", got, expected)
	}
}

func TestUpdateClientTomlViaAgent_NoOpSkipsWrite(t *testing.T) {
	current := "[client]\nremote_addr = \"router:2333\"\n"

	postCalled := false
	machine := startFakeAgent(t, &fakeAgent{
		getConfig: func(context.Context, *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
			return &agentpb.RatholeConfig{Toml: current}, nil
		},
		putConfig: func(context.Context, *agentpb.RatholeConfig) (*agentpb.PutRatholeConfigResponse, error) {
			postCalled = true
			return &agentpb.PutRatholeConfigResponse{Written: true}, nil
		},
	})

	s := &LocalSetupService{}
	err := s.updateClientTomlViaAgent(machine, func(existing string) (string, error) {
		return existing, nil // no change
	})
	if err != nil {
		t.Fatalf("updateClientTomlViaAgent: %v", err)
	}
	if postCalled {
		t.Errorf("expected no Put when transform returns identical content")
	}
}

func TestAgentClient_Uninstall_Success(t *testing.T) {
	machine := startFakeAgent(t, &fakeAgent{
		uninstall: func(ctx context.Context, _ *agentpb.UninstallRequest) (*agentpb.UninstallResponse, error) {
			if got := authToken(ctx); got != "test-token" {
				t.Errorf("missing/wrong bearer token: %q", got)
			}
			return &agentpb.UninstallResponse{Queued: true}, nil
		},
	})

	if err := NewAgentClient(machine).Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
}

func TestAgentClient_Uninstall_BubblesError(t *testing.T) {
	machine := startFakeAgent(t, &fakeAgent{
		uninstall: func(context.Context, *agentpb.UninstallRequest) (*agentpb.UninstallResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "missing /usr/local/bin/gopher-uninstall")
		},
	})

	err := NewAgentClient(machine).Uninstall(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should surface server detail: %v", err)
	}
}

// Ensure context cancellation propagates so a stuck agent doesn't block forever.
func TestAgentClient_GetRatholeConfig_RespectsContext(t *testing.T) {
	machine := startFakeAgent(t, &fakeAgent{
		getConfig: func(ctx context.Context, _ *agentpb.GetRatholeConfigRequest) (*agentpb.RatholeConfig, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				return &agentpb.RatholeConfig{}, nil
			}
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewAgentClient(machine).GetRatholeConfig(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
