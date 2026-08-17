.PHONY: build test test-verbose cover lint fmt vet check-fmt tidy kind-up kind-down test-e2e

BINARY := kubectl-safe_rollout
KIND_CLUSTER := safe-rollout

build:
	go build -o bin/$(BINARY) ./cmd/kubectl-safe_rollout

test:
	go test ./...

test-verbose:
	go test ./... -v

cover:
	go test ./internal/... -coverprofile=coverage.out -covermode=atomic
	go tool cover -func=coverage.out

fmt:
	gofmt -w .

check-fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "File non formattati:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	go vet ./...

lint: check-fmt vet
	golangci-lint run
	golangci-lint run --build-tags e2e

tidy:
	go mod tidy

# kind-up crea il cluster usato dagli scenari e2e, se non esiste gia'.
kind-up:
	@kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)" || kind create cluster --name $(KIND_CLUSTER) --wait 120s

kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

# test-e2e richiede un cluster kind attivo (make kind-up) e Docker in
# esecuzione: crea ed elimina risorse reali sul contesto
# kind-safe-rollout, vedi test/e2e/e2e_test.go. Isolato dal resto della
# suite dal build tag "e2e", per questo non e' incluso in `make test`
# ne' in CI (rules/test.md).
test-e2e:
	go test -tags e2e ./test/e2e/... -v -timeout 20m
