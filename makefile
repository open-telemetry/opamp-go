# Function to execute a command.
# Accepts command to execute as first parameter.
define exec-command
$(1)

endef

# Find all .proto files.
BASELINE_PROTO_FILES := $(wildcard internal/proto/*.proto)

all: test build-examples

.PHONY: test
test:
	go test -race ./...

.PHONY: build-examples
build-examples: build-example-agent build-example-server

build-example-agent:
	go build -o internal/examples/agent/bin/agent internal/examples/agent/main.go

build-example-server:
	go build -o internal/examples/server/bin/server internal/examples/server/main.go

run-examples: build-examples
	cd internal/examples/server && ./bin/server &
	@echo Server UI is running at http://localhost:4321/
	cd internal/examples/agent && ./bin/agent

# Generate Protobuf Go files.
.PHONY: gen-proto
gen-proto:
	$(foreach file,$(BASELINE_PROTO_FILES),$(call exec-command,docker run --rm -v${PWD}:${PWD} \
        -w${PWD} otel/build-protobuf:latest --proto_path=${PWD}/internal/proto/ \
        --go_out=${PWD}/internal/proto/ -I${PWD}/internal/proto/ ${PWD}/$(file)))

	cp -R internal/proto/github.com/open-telemetry/opamp-go/protobufs/* protobufs/
	rm -rf internal/proto/github.com/

.PHONY: gomoddownload
gomoddownload:
	go mod download
