.PHONY: build test race fuzz test-e2e-versions test-verbose cover lint fmt vet check-fmt tidy release-check release-snapshot kind-up kind-down test-e2e

BINARY := kubectl-safe_rollout
KIND_CLUSTER := safe-rollout

build:
	go build -o bin/$(BINARY) ./cmd/kubectl-safe_rollout

test:
	go test ./...

# The watch loop is the project's only concurrent code, so keep it covered by the race detector.
race:
	go test -race ./...

# CI does not run time-boxed, non-deterministic fuzzing; contributors can run
# this target before changing the pattern package.
fuzz:
	go test -run=Fuzz -fuzz=FuzzMissingConfigObject -fuzztime=10s ./internal/diagnose/pattern/
	go test -run=Fuzz -fuzz=FuzzImagePullFailure -fuzztime=10s ./internal/diagnose/pattern/
	go test -run=Fuzz -fuzz=FuzzLivenessKilling -fuzztime=10s ./internal/diagnose/pattern/
	go test -run=Fuzz -fuzz=FuzzReadinessFailure -fuzztime=10s ./internal/diagnose/pattern/
	go test -run=Fuzz -fuzz=FuzzFailedScheduling -fuzztime=10s ./internal/diagnose/pattern/
	go test -run=Fuzz -fuzz=FuzzQuotaExceeded -fuzztime=10s ./internal/diagnose/pattern/

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

# E2E_MINORS are the Kubernetes minor versions the project claims to support.
# They are pinned to exact patch releases so a run is reproducible and so the
# README can state what was actually exercised rather than "recent versions".
E2E_MINORS := v1.36.1 v1.35.5 v1.34.8

# test-e2e-versions repeats the whole e2e suite against one throwaway cluster
# per supported minor. It exists because the messages this tool classifies are
# emitted by kubelet, the scheduler and the container runtime, and those are
# exactly the components that change between minors: a suite that only ever
# runs on one version proves the classification works there and nowhere else.
#
# Each cluster is deleted afterwards, including on failure, so a broken run
# does not leave several clusters behind eating memory.
test-e2e-versions:
	@set -e; \
	for version in $(E2E_MINORS); do \
		cluster="sr-$$(echo $$version | tr . -)"; \
		echo "=== Kubernetes $$version (cluster $$cluster) ==="; \
		kind create cluster --name "$$cluster" --image "kindest/node:$$version" --wait 180s; \
		status=0; \
		E2E_CONTEXT="kind-$$cluster" go test -tags e2e ./test/e2e/... -timeout 45m || status=$$?; \
		kind delete cluster --name "$$cluster"; \
		if [ "$$status" -ne 0 ]; then echo "e2e failed on Kubernetes $$version"; exit "$$status"; fi; \
	done

# test-e2e requires an active kind cluster (make kind-up) and Docker running:
# it creates and deletes real resources in the kind-safe-rollout context; see
# test/e2e/e2e_test.go. The "e2e" build tag isolates it from the rest of the
# suite, so it is not included in `make test` or CI (rules/test.md).
test-e2e:
	go test -tags e2e ./test/e2e/... -v -timeout 20m
