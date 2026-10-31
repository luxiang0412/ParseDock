# Minimal ParseDock Java example.
#
#   export PARSEDOCK_TOKEN=...        # key from deploy/.env (PARSEDOCK_API_KEYS)
#   make run                          # parses the bundled fixture
#   make run FILE=~/docs/report.pdf
#
API  ?= $(if $(PARSEDOCK_API),$(PARSEDOCK_API),http://127.0.0.1:8080)
FILE ?= $(abspath ../../benchmark/fixtures/generated/pdf_text_en_3p.pdf)
MODE ?= auto

.PHONY: run install-client clean

run: install-client
	PARSEDOCK_API="$(API)" PARSEDOCK_MODE="$(MODE)" \
	  mvn -q -f pom.xml compile exec:java -Dexec.args="$(FILE)"

install-client:
	@mvn -q -f ../../sdk/java-generated/pom.xml -DskipTests install

clean:
	rm -rf target
