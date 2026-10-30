GO ?= go

.PHONY: fmt vet test test-integration test-e2e test-performance historical-replay tracker-check tracker-test build clean

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

test-integration:
	$(GO) test -tags=integration ./internal/persistence

test-e2e:
	$(GO) test -tags=e2e ./tests/e2e/...

test-performance:
	$(GO) test -tags=performance -run=^$$ -bench=BenchmarkAnalyzeImpact10kTests -benchtime=1x ./tests/performance/...

historical-replay:
	$(GO) run ./tests/historical-replay

tracker-check:
	python scripts/generate-tracker.py --check

tracker-test:
	python scripts/test_generate_tracker.py

build:
	$(GO) build -trimpath -ldflags="-s -w" -o bin/ivp ./cmd/ivp
	$(GO) build -trimpath -ldflags="-s -w" -o bin/ivp-api ./cmd/ivp-api

clean:
	$(GO) clean ./...

