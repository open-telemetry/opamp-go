# Makefile for OpAMP examples

GOCMD?= go

# SRC_ROOT is the top of the source tree (project root)
SRC_ROOT := $(shell git rev-parse --show-toplevel)

# Build targets
.PHONY: build-examples
build-examples: build-example-agent build-example-supervisor build-example-server

.PHONY: build-example-agent
build-example-agent:
	$(GOCMD) build -o agent/bin/agent agent/main.go

.PHONY: build-example-supervisor
build-example-supervisor:
	$(GOCMD) build -o supervisor/bin/supervisor supervisor/main.go

.PHONY: build-example-server
build-example-server:
	$(GOCMD) build -o server/bin/server server/main.go

.PHONY: run-examples
run-examples: build-examples
	server/bin/server &
	@echo Server UI is running at http://localhost:4321/
	agent/bin/agent

# Docker targets

.PHONY: build-docker-server
build-docker-server:
	cd $(SRC_ROOT) && docker build -f internal/examples/Dockerfile.server -t opamp-server-example:$(shell git rev-parse --short HEAD) .

.PHONY: build-docker-agent
build-docker-agent:
	cd $(SRC_ROOT) && docker build -f internal/examples/Dockerfile.agent -t opamp-agent-example:$(shell git rev-parse --short HEAD) .

.PHONY: run-docker-server
run-docker-server: build-docker-server
	docker run --rm -p 4320:4320 -p 4321:4321 opamp-server-example:$(shell git rev-parse --short HEAD) & \
	echo "Server UI is running at http://$(shell hostname):4321/"

.PHONY: run-docker-agent
run-docker-agent: build-docker-agent
	docker run --rm --network host opamp-agent-example:$(shell git rev-parse --short HEAD) --endpoint wss://$(shell hostname):4320/v1/opamp --tls-insecure_skip_verify

.PHONY: run-docker
run-docker: build-docker-server build-docker-agent
	@docker run --rm --name opamp-server -p 4320:4320 -p 4321:4321 opamp-server-example:$(shell git rev-parse --short HEAD) & \
	SERVER_PID=$$!; \
	echo "Server UI is running at http://$(shell hostname):4321/"; \
	trap 'docker stop opamp-server 2>/dev/null || true; kill $$SERVER_PID 2>/dev/null || true' INT TERM; \
	docker run --rm --name opamp-agent --network host opamp-agent-example:$(shell git rev-parse --short HEAD) --endpoint wss://$(shell hostname):4320/v1/opamp --tls-insecure_skip_verify; \
	docker stop opamp-server 2>/dev/null || true

.PHONY: docker-compose-up
docker-compose-up:
	cd $(SRC_ROOT)/internal/examples && docker compose up --build

.PHONY: docker-compose-down
docker-compose-down:
	cd $(SRC_ROOT)/internal/examples && docker compose down

.PHONY: docker-compose-logs
docker-compose-logs:
	cd $(SRC_ROOT)/internal/examples && docker compose logs -f

.PHONY: docker-compose-scale
docker-compose-scale:
	@echo "Usage: make docker-compose-scale AGENTS=<number>"
	@if [ -z "$(AGENTS)" ]; then \
		echo "Error: AGENTS variable not set"; \
		exit 1; \
	fi
	cd $(SRC_ROOT)/internal/examples && docker compose up --scale opamp-agent=$(AGENTS) -d
