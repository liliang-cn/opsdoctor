# opsdoctor — build / dev / deploy
# The committed web/dist is embedded into the binary, so `build` does NOT require
# Node. Run `make web` only after changing the front-end.

BIN        := opsdoctor
HOST       ?= linode-jp
REMOTE_DIR ?= /opt/opsdoctor
DIST       := dist/opsdoctor-linux-amd64

.PHONY: build web build-linux run fmt vet test check deploy clean help

help: ## list targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n",$$1,$$2}'

build: ## build local binary (uses committed web/dist)
	go build -o $(BIN) ./cmd/opsdoctor

web: ## rebuild the embedded web UI (after front-end changes)
	cd web && npm install && npm run build

build-linux: ## cross-compile a static linux/amd64 binary (embeds web/dist)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(DIST) ./cmd/opsdoctor

run: build ## build + serve locally (expects OPSDOCTOR_* env set)
	./$(BIN) serve

fmt: ## gofmt the source
	gofmt -w cmd internal web/embed.go

vet: ## go vet
	go vet ./...

test: ## go test
	go test ./...

check: fmt vet test ## fmt + vet + test

deploy: build-linux ## cross-compile + push binary + restart service on HOST (must be provisioned; see docs/ONBOARDING.md)
	ssh $(HOST) 'mkdir -p $(REMOTE_DIR)'
	scp -q $(DIST) $(HOST):$(REMOTE_DIR)/opsdoctor.new
	ssh $(HOST) 'chmod +x $(REMOTE_DIR)/opsdoctor.new && mv $(REMOTE_DIR)/opsdoctor.new $(REMOTE_DIR)/opsdoctor && systemctl restart $(BIN) && sleep 3 && systemctl is-active $(BIN)'

push-db: ## copy the local knowledge DB to HOST (rebuild-free deploy of the index)
	scp -q data/knowledge.db $(HOST):$(REMOTE_DIR)/data/knowledge.db
	ssh $(HOST) 'systemctl restart $(BIN)'

clean: ## remove build artifacts
	rm -f $(BIN) $(DIST)
