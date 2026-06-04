#!/usr/bin/env bash
# Regenerate the agent gRPC stubs from proto/agent/v1/agent.proto.
#
# The generated *.pb.go files in internal/agentpb/ are committed to the repo, so
# a normal `go build` needs no protobuf toolchain. Only run this after editing a
# .proto file.
#
# Requires buf and the Go plugins on PATH:
#   go install github.com/bufbuild/buf/cmd/buf@latest
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
set -euo pipefail

cd "$(dirname "$0")/../proto"
export PATH="$(go env GOPATH)/bin:$PATH"

echo "==> buf lint"
buf lint

echo "==> buf generate"
buf generate

echo "==> gofmt generated"
gofmt -w ../internal/agentpb

echo "Done. Generated files in internal/agentpb/ — commit them alongside the .proto change."
