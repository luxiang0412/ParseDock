# Releasing

Two artefact families share one version: the service images and the SDKs. The
version lives in four places and `scripts/check_versions.py` fails when they drift:

| Artefact | Source of truth |
| --- | --- |
| binary / API | `api/openapi.yaml` `info.version`, `cmd/docparse` serviceVersion |
| Python | `sdk/python/pyproject.toml` + `parsedock_sdk.__version__` |
| Java | generated `sdk/java-generated/pom.xml` (`artifactVersion` in the Makefile) |

## 1. Cut the tag

```bash
# on main, after CI is green
git tag -a v0.1.0 -m "ParseDock v0.1.0"
git push origin v0.1.0
```

Pushing a `v*` tag triggers `release-python.yml` and `release-maven.yml`. Both can
also be started by hand (`workflow_dispatch`) when a re-run is needed.

## 2. Python → PyPI

**Trusted Publishing (no secret).** On PyPI create a pending publisher with

| Field | Value |
| --- | --- |
| PyPI Project Name | `parsedock` |
| Owner | `luxiang0412` |
| Repository name | `ParseDock` |
| Workflow name | `release-python.yml` |
| Environment name | `pypi` |

Then create the environment `pypi` in *Settings → Environments* (add required
reviewers if a human should approve each publish). The workflow builds sdist +
wheel, runs `twine check`, installs the wheel in a clean venv and publishes over
OIDC with `id-token: write` — there is no `PYPI_API_TOKEN` anywhere.

If you leave the PyPI environment field blank, delete the `environment: pypi`
line from the workflow as well; the two must match.

## 3. Java → Maven Central

`mvn -Prelease deploy` publishes; the default profile stays signing-free so tests
and CI need no key. The `release` profile adds the sources jar, the javadoc jar,
GPG signing and `central-publishing-maven-plugin` (`autoPublish=false`, so the
deployment waits for a human in the Central Portal).

**Namespace.** Central requires you to prove ownership of the POM's `groupId`:

| groupId | Verification |
| --- | --- |
| `com.luxiangs` (current) | DNS TXT record on `luxiangs.com` (the domain has to be yours) |
| `io.github.luxiang0412` | verified through the GitHub account, no DNS needed |

To switch, change `groupId=` in the `make sdk` line in the `Makefile`, run
`make sdk` (regeneration + `scripts/patch_java_pom.py`), and re-check
`docs/SDK.md`.

**Repository secrets** (*Settings → Secrets and variables → Actions*):

| Secret | Value |
| --- | --- |
| `MAVEN_CENTRAL_USERNAME` / `MAVEN_CENTRAL_TOKEN` | Central Portal user token (*Account → Generate User Token*) |
| `GPG_PRIVATE_KEY` | `gpg --armor --export-secret-keys <key-id>` |
| `MAVEN_GPG_PASSPHRASE` | the key passphrase |

Create an environment named `central` (the workflow uses it) if you want the same
manual-approval gate as PyPI.

After the workflow deploys, open the Central Portal, review the pending bundle and
publish it. Artifacts appear in Maven Central a few minutes later.

## 4. Service images

Images are built from the tag (see `docs/DEPLOYMENT.md`). The control-plane image
is distributable; **the worker image is not** until licence item L1
(`libx265`, GPL-2.0 inside the xberg distribution) is resolved — see
`docs/CHANGELOG.md` → Known limitations.

## 5. After the release

* `docs/CHANGELOG.md` gets a new section (the release notes are copied from it).
* `scripts/check_versions.py` must pass, which means the OpenAPI `info.version`,
  the binary, `pyproject.toml` and the generated POM were bumped together.
* Re-run `scripts/sdk-check.sh` and, for a service release, the acceptance suites
  in `docs/CI.md`.
