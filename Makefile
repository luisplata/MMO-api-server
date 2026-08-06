# MMO API Server — build & codegen tooling
#
# Pinned codegen toolchain (design D6):
#   protoc            v35.1       (protocolbuffers/protobuf)
#   protoc-gen-go     v1.36.11    (google.golang.org/protobuf)
#   buf               v1.72.0     (bufbuild/buf) — C# via remote plugin buf.build/protocolbuffers/csharp:v35.1
#
# Install on Windows:
#   protoc:        download protoc-35.1-win64.zip from the protobuf releases page, add bin/ to PATH
#   protoc-gen-go: go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
#   buf:           go install github.com/bufbuild/buf/cmd/buf@v1.72.0
#
# The generated output under proto/v1/gen/ is COMMITTED: the Unity dev
# never needs protoc, buf, or any protobuf tooling to consume the client.

PROTOC        ?= protoc
BUF           ?= buf
PROTO_DIR     := proto
GO_OUT        := proto/v1/gen/go
TESTFIX_DIR   := internal/protocol/testfixture

.PHONY: proto proto-go proto-csharp test build vet fmt tidy all

## all: regenerate, tidy, format, vet, build, test
all: proto tidy fmt vet build test

## proto: regenerate both committed Go and C# bindings from the .proto contract
proto: proto-go proto-csharp

## proto-go: regenerate committed Go bindings (protoc v35.1 + protoc-gen-go v1.36.11)
proto-go:
	$(PROTOC) -I $(PROTO_DIR) \
		--go_out=$(GO_OUT) \
		--go_opt=paths=source_relative \
		$(PROTO_DIR)/v1/world.proto
	$(PROTOC) -I $(TESTFIX_DIR) \
		--go_out=$(TESTFIX_DIR) \
		--go_opt=paths=source_relative \
		$(TESTFIX_DIR)/mini.proto

## proto-csharp: regenerate committed C# bindings
## (buf remote plugin == the official protoc-gen-csharp v35.1; no local
##  C# compiler needed — the plugin runs on buf.build)
proto-csharp:
	$(BUF) generate $(PROTO_DIR)

## tidy: sync go.mod/go.sum with the generated code's dependencies
tidy:
	go mod tidy

## test: run the full Go test suite
test:
	go test ./...

## build: compile everything
build:
	go build ./...

## vet: static checks
vet:
	go vet ./...

## fmt: format all Go source
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './.git/*')
