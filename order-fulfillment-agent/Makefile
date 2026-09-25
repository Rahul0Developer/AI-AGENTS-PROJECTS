GO ?= go
.PHONY: all build test vet demo serve smoke docker clean
all: vet test build
build:            ; $(GO) build -o bin/agent ./cmd/agent
test:             ; $(GO) test ./...
vet:              ; $(GO) vet ./...
demo: build       ; ./bin/agent demo
serve: build      ; PORT=8080 POLL_SECONDS=15 ./bin/agent serve
smoke:            ; BASE_URL=$${BASE_URL:-http://localhost:8080} ./scripts/smoke-test.sh
docker:           ; docker build -t order-fulfillment-agent .
clean:            ; rm -rf bin data
