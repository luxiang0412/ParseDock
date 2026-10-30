# ParseDock — Phase 0 developer entry points.
GO ?= go
BIN := runtime/bin/docparse
PY_FIX := runtime/engines/venv-fixtures/bin/python
COMPOSE := docker compose -f deploy/compose.cpu.yaml

.PHONY: help build test vet fmt run-smoke fixtures acceptance acceptance-real bench sdk java-sdk sdk-test images up down logs clean

help: ## list targets
	@awk -F':.*## ' '/^[a-z-]+:.*## /{printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## build the single binary
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/docparse
	@$(BIN) --check | head -20

fmt: ## gofmt
	gofmt -w cmd internal migrations

vet: ## go vet
	$(GO) vet ./...

test: vet ## unit tests (Go)
	$(GO) test ./... -count=1

fixtures: ## regenerate the synthetic benchmark corpus (needs venv-fixtures)
	$(PY_FIX) benchmark/scripts/gen_fixtures.py

acceptance: ## T03–T16 lifecycle acceptance with the mock engine
	python3 tests/integration/acceptance.py --profile lifecycle

acceptance-real: ## T01/T02/T11 against real engines (native,xberg,docling-rs)
	python3 tests/integration/acceptance.py --profile real --engines native,xberg,docling-rs

bench: ## quality + latency + concurrency (requires engine servers)
	$(PY_FIX) benchmark/scripts/bench.py all --engines native,xberg,docling-rs,docling-serve,tika,mineru --repeats 5

gpu-bench: ## GPU verification (docling cpu/cuda, mineru flash)
	$(PY_FIX) benchmark/scripts/gpu_bench.py --repeats 3

sdk: ## regenerate Python/Java SDKs from api/openapi.yaml
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local openapitools/openapi-generator-cli validate -i /local/api/openapi.yaml
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local openapitools/openapi-generator-cli generate -i /local/api/openapi.yaml -g python -o /local/sdk/python-generated --package-name parsedock_client --additional-properties=library=urllib3,projectName=parsedock
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local openapitools/openapi-generator-cli generate -i /local/api/openapi.yaml -g java -o /local/sdk/java-generated --additional-properties=groupId=dev.parsedock,artifactId=parsedock-client,artifactVersion=0.1.0,library=okhttp-gson

sdk-test: ## run the Python SDK lifecycle test against a local stack
	python3 sdk/python/test_sdk.py

images: ## build control-plane and CPU worker images
	docker build -f docker/Dockerfile -t parsedock:0.1.0 .
	docker build -f docker/Dockerfile.engines -t parsedock-worker-cpu:0.1.0 .

up: ## start the CPU compose stack
	$(COMPOSE) --env-file deploy/.env up -d

down: ## stop the compose stack
	$(COMPOSE) --env-file deploy/.env down

logs: ## tail compose logs
	$(COMPOSE) logs -f --tail=50

clean: ## remove local runtime state (keeps engines/models)
	rm -rf runtime/data runtime/work runtime/acceptance runtime/sdk-test-data runtime/bench-work
