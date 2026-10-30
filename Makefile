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

fmt-check: ## fail when files are not gofmt'd (CI)
	@out=$$(gofmt -l cmd internal migrations); if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

schema: ## publish the Document IR JSON Schema to api/
	cp internal/documentir/schema/document-ir-0.1.schema.json api/document-ir-0.1.schema.json

verify-pins: ## check go.mod / Dockerfiles / workflows against images.lock.json
	python3 scripts/verify_pins.py

lint-workflows: ## actionlint on .github/workflows (pinned binary)
	bash scripts/install-actionlint.sh
	runtime/bin/actionlint -no-color -shellcheck= .github/workflows/*.yml

vet: ## go vet
	$(GO) vet ./...

test: vet ## unit + integration tests (Go)
	$(GO) test ./... -count=1
	python3 tests/unit/test_score.py

contract: ## PR-00: OpenAPI/SDK/router contract checks
	python3 tests/contract/openapi_contract.py

ci: ## run the same phases CI runs (static,unit,contract,lifecycle,image)
	bash scripts/ci-local.sh all

ci-phase: ## run one CI phase (PHASE=static|unit|contract|lifecycle|image|engines|container)
	bash scripts/ci-local.sh $(PHASE)

container-smoke: ## end-to-end container smoke with real engines
	bash scripts/container-smoke.sh

pr00: contract ## runtime contract checks (PR-00/PR-02/PR-07 scenarios)
	python3 tests/integration/pr00_contract.py
	python3 tests/integration/backup_restore.py
	python3 tests/integration/pr02_contract.py
	python3 tests/integration/pr07_tls.py
	python3 tests/integration/pr09_metrics.py

load: ## cluster-lite load check (mock engine): make load JOBS=200 CONC=32
	bash scripts/cluster-lite-load.sh $(or $(JOBS),120) $(or $(CONC),12)

gpu-check: ## GPU worker image end-to-end (needs a GPU host): make gpu-check
	python3 tests/integration/pr08_gpu.py --build

score: ## regenerate benchmark scores + docs tables
	$(PY_FIX) benchmark/scripts/score.py --write
	python3 tests/unit/test_score.py

license-audit: ## SBOM + GPL gate for the built images (external distribution)
	bash scripts/license_audit.sh parsedock:0.1.0 parsedock-worker-cpu:0.1.0

fixtures: ## regenerate fixtures + the 110-document corpus (needs venv-fixtures)
	$(PY_FIX) benchmark/scripts/gen_fixtures.py --corpus 110

acceptance: ## T03–T16 lifecycle acceptance with the mock engine
	python3 tests/integration/acceptance.py --profile lifecycle

acceptance-real: ## T01/T02/T11 against real engines (native,xberg,docling-rs)
	python3 tests/integration/acceptance.py --profile real --engines native,xberg,docling-rs

bench: ## quality + latency + concurrency (requires engine servers)
	$(PY_FIX) benchmark/scripts/bench.py all --engines native,xberg,docling-rs,docling-serve,tika,mineru --repeats 5

gpu-bench: ## GPU verification (docling cpu/cuda, mineru flash)
	$(PY_FIX) benchmark/scripts/gpu_bench.py --repeats 3

GEN_IMAGE := $(shell python3 -c 'import json;print(json.load(open("images.lock.json"))["images"]["openapi_generator"])')

sdk: ## regenerate Python/Java SDKs from api/openapi.yaml (deterministic)
	docker run --rm -v $$PWD:/local $(GEN_IMAGE) validate -i /local/api/openapi.yaml
	rm -rf sdk/python-generated sdk/java-generated
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local $(GEN_IMAGE) generate -i /local/api/openapi.yaml -g python -o /local/sdk/python-generated --package-name parsedock_client --additional-properties=library=urllib3,projectName=parsedock,hideGenerationTimestamp=true
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local $(GEN_IMAGE) generate -i /local/api/openapi.yaml -g java -o /local/sdk/java-generated --additional-properties=groupId=dev.parsedock,artifactId=parsedock-client,artifactVersion=0.1.0,library=okhttp-gson,hideGenerationTimestamp=true

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

metrics: ## observability contract: metrics exist, throttling counted, no content leaks
	python3 tests/integration/pr09_metrics.py

sdk-check: ## Python + CLI + Java SDK build and one version across all artefacts
	bash scripts/sdk-check.sh

drill: ## online backup + restore drill with RTO (PR-12)
	python3 tests/integration/pr12_drill.py

gate: ## engine quality gate vs benchmark/results/baseline.json
	python3 benchmark/scripts/gate.py

gate-baseline: ## record the current benchmark summary as the quality baseline
	python3 benchmark/scripts/gate.py --update

clean: ## remove local runtime state (keeps engines/models)
	rm -rf runtime/data runtime/work runtime/acceptance runtime/sdk-test-data runtime/bench-work
