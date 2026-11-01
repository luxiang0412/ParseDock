# ParseDock — developer entry points (`make help` lists them).
GO ?= go
# Explicit package patterns: `./...` would also pick up Go packages that npm drops
# into web/node_modules (e.g. flatted ships one) once the playground deps are installed.
GO_PKGS := ./cmd/... ./internal/... ./migrations/...
BIN := runtime/bin/docparse
PY_FIX := runtime/engines/venv-fixtures/bin/python
COMPOSE := docker compose -f deploy/compose.cpu.yaml

.PHONY: help build fmt fmt-check schema verify-pins lint vulncheck lint-workflows vet test contract ci ci-phase \
	container-smoke pr00 load gpu-check score license-audit fixtures acceptance acceptance-real \
	bench gpu-bench sdk sdk-test images up down logs metrics sdk-check drill gate gate-baseline \
	web-install web-dev web-test web-e2e web-e2e-compose playground-up playground-down clean

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

lint: ## golangci-lint (Go) + ruff (Python), pinned versions
	bash scripts/install-golangci-lint.sh >/dev/null
	runtime/bin/golangci-lint run $(GO_PKGS)
	bash scripts/install-ruff.sh >/dev/null
	runtime/ci-venv/bin/ruff check tests scripts benchmark sdk/python examples

vulncheck: ## govulncheck with the pinned toolchain (refuses any other go on PATH)
	bash scripts/govulncheck.sh

lint-workflows: ## actionlint on .github/workflows (pinned binary)
	bash scripts/install-actionlint.sh
	runtime/bin/actionlint -no-color -shellcheck= .github/workflows/*.yml

vet: ## go vet
	$(GO) vet $(GO_PKGS)

test: vet ## unit + integration tests (Go)
	$(GO) test $(GO_PKGS) -count=1
	python3 tests/unit/test_score.py

contract: ## PR-00: OpenAPI/SDK/router contract checks
	python3 tests/contract/openapi_contract.py

ci: ## run the CI phases static,unit,contract,lifecycle,image (CI also runs web + sdk jobs)
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

acceptance: ## lifecycle acceptance T03–T17 except T11 (14 cases) with the mock engine
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
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local $(GEN_IMAGE) generate -i /local/api/openapi.yaml -g python -o /local/sdk/python-generated --package-name parsedock_client --additional-properties=library=urllib3,projectName=parsedock,hideGenerationTimestamp=true,enumUnknownDefaultCase=true
	docker run --rm -u $$(id -u):$$(id -g) -v $$PWD:/local $(GEN_IMAGE) generate -i /local/api/openapi.yaml -g java -o /local/sdk/java-generated --additional-properties=groupId=com.luxiangs,artifactId=parsedock-client,artifactVersion=0.1.2,library=okhttp-gson,hideGenerationTimestamp=true,enumUnknownDefaultCase=true
	# Central needs metadata, sources/javadoc and a release profile the generator
	# does not emit; the patch is idempotent and rerun after every regeneration.
	python3 scripts/patch_java_pom.py sdk/java-generated/pom.xml
	# The okhttp-gson template interpolates file names into Content-Disposition raw,
	# which OkHttp rejects for non-ASCII names (issue #66).
	python3 scripts/patch_java_client.py sdk/java-generated/src/main/java/org/openapitools/client/ApiClient.java

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

# ---------------------------------------------------------------- playground (web/)
web-install: ## install playground dependencies (lockfile-exact)
	cd web && npm ci --no-audit --no-fund

web-dev: ## run the playground dev server (needs PARSEDOCK_DEV_KEY and a running API)
	cd web && npm run dev

web-test: ## playground lint, typecheck, unit tests and production build
	cd web && npm run api:types:check && npm run lint && npm run typecheck && npm test && npm run build
	cd web && node scripts/check-dist-secrets.mjs

web-e2e: ## Playwright E2E against a local backend (make build first; E2E_ENGINES=native,xberg for a real PDF)
	cd web && npm run build && npx playwright test

web-e2e-compose: ## Playwright E2E against the running Docker Compose stack (PLAYGROUND_URL=http://127.0.0.1:8090)
	cd web && E2E_COMPOSE_URL=$${PLAYGROUND_URL:-http://127.0.0.1:8090} npx playwright test

playground-up: ## start the CPU stack + playground (needs deploy/.env)
	docker compose -f deploy/compose.cpu.yaml -f deploy/compose.playground.yaml \
		--env-file deploy/.env --profile playground up -d --build

playground-down: ## stop the playground profile
	docker compose -f deploy/compose.cpu.yaml -f deploy/compose.playground.yaml \
		--env-file deploy/.env --profile playground down

clean: ## remove local runtime state (keeps engines/models)
	rm -rf runtime/data runtime/work runtime/acceptance runtime/sdk-test-data runtime/bench-work
