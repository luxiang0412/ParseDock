# Minimal ParseDock Python example.
#
#   export PARSEDOCK_TOKEN=...        # key from deploy/.env (PARSEDOCK_API_KEYS)
#   make run                          # parses the bundled fixture
#   make run FILE=~/docs/report.pdf
#
API  ?= $(if $(PARSEDOCK_API),$(PARSEDOCK_API),http://127.0.0.1:8080)
FILE ?= $(abspath ../../benchmark/fixtures/generated/pdf_text_en_3p.pdf)
MODE ?= auto
VENV ?= .venv

.PHONY: run venv clean

run: venv
	.venv/bin/python parse.py "$(FILE)" --api "$(API)" --mode "$(MODE)"

venv:
	@test -x .venv/bin/python || (python3 -m venv .venv && .venv/bin/pip -q install -e ../../sdk/python)

clean:
	rm -rf .venv *.md *.txt *.json
