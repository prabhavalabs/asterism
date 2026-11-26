.PHONY: gen dev build test lint clean

gen:
	buf generate

dev-go:
	go run ./cmd/asterism serve

dev-web:
	cd web && pnpm dev

build-web:
	cd web && pnpm build
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -R web/dist/. internal/web/dist/

build: build-web
	mkdir -p bin
	go build -tags embed_web -trimpath -ldflags "-s -w" -o ./bin/asterism ./cmd/asterism

test:
	go test -race ./...
	cd web && pnpm test

lint:
	go vet ./...
	cd web && pnpm lint && pnpm typecheck

clean:
	rm -rf bin internal/web/dist web/dist

# --- asterism-agent ---------------------------------------------------------
# Budgets (BLUEPRINT §16): stripped binary <= 11 MiB, image <= 12 MB.
.PHONY: agent agent-image agent-image-size agent-it-images e2e-agent agent-footprint

AGENT_OUT ?= bin
AGENT_VERSION ?= dev
AGENT_IMAGE ?= asterism-agent:dev
AGENT_MAX_IMAGE_BYTES ?= 12000000
MINUTES ?= 5
CONTAINERS ?= 50

# Static agent binary for GOOS/GOARCH (default: this machine), e.g.
#   make agent GOOS=linux GOARCH=arm64 AGENT_OUT=dist
agent:
	mkdir -p $(AGENT_OUT)
	CGO_ENABLED=0 go build -trimpath -tags netgo,osusergo \
		-ldflags "-s -w -X main.version=$(AGENT_VERSION)" \
		-o $(AGENT_OUT)/asterism-agent ./cmd/asterism-agent

# Container image for this machine's architecture, loaded into Docker. For
# a multi-arch push use docker buildx build --platform linux/amd64,linux/arm64.
agent-image:
	docker buildx build -f deploy/agent/Dockerfile --load \
		--build-arg VERSION=$(AGENT_VERSION) -t $(AGENT_IMAGE) .

# Fails when the image's root filesystem exceeds AGENT_MAX_IMAGE_BYTES.
agent-image-size:
	@c=$$(docker create $(AGENT_IMAGE)) && \
	size=$$(docker export $$c | wc -c | tr -d ' ') && docker rm $$c >/dev/null && \
	echo "$(AGENT_IMAGE): $$size bytes (limit $(AGENT_MAX_IMAGE_BYTES))" && \
	test $$size -le $(AGENT_MAX_IMAGE_BYTES)

# Hub and agent images used by e2e-agent and agent-footprint.
agent-it-images:
	ASTERISM_IT_DIR=$(CURDIR) docker compose -f scripts/agent-it/compose.yaml build hub agent

# Integration test against Docker (hub + hardened agent + socket proxy +
# seed containers); removes everything it starts.
e2e-agent:
	go test -tags integration -count=1 -v -timeout 20m ./scripts/agent-it

# Footprint benchmark: make agent-footprint MINUTES=5 CONTAINERS=50
agent-footprint: agent-it-images
	scripts/agent-footprint.sh $(MINUTES) $(CONTAINERS)
