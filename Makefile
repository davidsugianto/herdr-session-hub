.PHONY: help run stop pair build web web-dev test fmt check

# Extra flags for run/pair, e.g.
#   make run  ARGS='-listen 0.0.0.0:7878 -allow-host my-mac.local'
#   make pair ARGS='-url http://my-mac.local:7878'
ARGS ?=
# Port `make stop` looks at; change it if you run the hub with another -listen.
PORT ?= 7878

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-9s %s\n", $$1, $$2}'

run: ## start the hub (http://127.0.0.1:7878)
	go run ./cmd/hub serve $(ARGS)

stop: ## stop the hub listening on PORT (default 7878)
	@pids=$$(lsof -ti tcp:$(PORT) -sTCP:LISTEN); \
	if [ -z "$$pids" ]; then echo "nothing is listening on port $(PORT)"; \
	else kill $$pids && echo "stopped hub (pid $$pids) on port $(PORT)"; fi

pair: ## print a one-time pairing code and QR link
	go run ./cmd/hub pair $(ARGS)

web: ## rebuild web/dist (committed, embedded in the binary)
	cd web && pnpm install --frozen-lockfile && pnpm build

web-dev: ## Vite dev server, proxies /api to 127.0.0.1:7878
	cd web && pnpm dev

build: web ## rebuild the web app, then the hub binary (./hub)
	go build -o hub ./cmd/hub

test: ## Go and web unit tests
	go test -race ./...
	cd web && pnpm test

fmt: ## fail if Go files are not gofmt-formatted
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "gofmt needed"; exit 1; }

check: fmt ## everything: format, vet, build, tests, typecheck, web build
	go vet ./...
	go build ./...
	go test -race -count=1 ./...
	cd web && pnpm install --frozen-lockfile && pnpm typecheck && pnpm test && pnpm build
