.PHONY: build test test-verbose cover lint fmt vet check-fmt tidy release-check release-snapshot kind-up kind-down test-e2e

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

# Real releases are produced only by pushing a v* tag. These targets catch a
# broken release configuration before the tag is pushed, while it is cheap to fix.
release-check:
	goreleaser check

release-snapshot:
	goreleaser release --snapshot --clean

# kind-up creates the cluster used by e2e scenarios, if it does not exist.
kind-up:
	@kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)" || kind create cluster --name $(KIND_CLUSTER) --wait 120s

kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

# test-e2e requires an active kind cluster (make kind-up) and Docker running:
# it creates and deletes real resources in the kind-safe-rollout context; see
# test/e2e/e2e_test.go. The "e2e" build tag isolates it from the rest of the
# suite, so it is not included in `make test` or CI (rules/test.md).
test-e2e:
	go test -tags e2e ./test/e2e/... -v -timeout 20m
