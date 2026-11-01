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
`make sdk` (regeneration + `scripts/patch_java_pom.py` and
`scripts/patch_java_client.py`), and re-check `docs/SDK.md`.

**Repository secrets** (*Settings → Secrets and variables → Actions*):

| Secret | Value |
| --- | --- |
| `MAVEN_CENTRAL_USERNAME` / `MAVEN_CENTRAL_TOKEN` | Central Portal user token (*Account → Generate User Token*) |
| `GPG_PRIVATE_KEY` | `gpg --armor --export-secret-keys <key-id>` |
| `MAVEN_GPG_PASSPHRASE` | the key passphrase |

Both workflows use the **`pypi` environment** (the repository keeps every release
secret there, so there is one approval gate and no duplicated values). To split
them, create a `central` environment, move the four `MAVEN_*`/GPG secrets into it,
and change `environment:` in `release-maven.yml`.

After the workflow deploys, open the Central Portal, review the pending bundle and
publish it. Artifacts appear in Maven Central a few minutes later.

## 4. Service images

Images are built from the tag (see `docs/DEPLOYMENT.md`). The control-plane image
is distributable; **the worker image is not** until licence item L1
(`libx265`, GPL-2.0 inside the xberg distribution) is resolved — see
`docs/CHANGELOG.md` → Known limitations.

## 4a. Failures we hit on the first release (and the fix)

| Symptom | Cause | Fix in the workflow / repo |
| --- | --- | --- |
| `gpg: signing failed: Bad passphrase` | `MAVEN_GPG_PASSPHRASE` had a trailing newline, or was the passphrase of a different key | a preflight step now prints the imported key id and signs a two-line payload, so the error names the secret; set secrets with `printf '%s' "$P" \| gh secret set … < /dev/stdin` |
| Upload fails with `Invalid request. Status: 401`, log line `namecode: ${env.MAVEN_CENTRAL_USERNAME}` | `setup-java` writes `${env.MAVEN_CENTRAL_USERNAME}` into `settings.xml`; Maven resolves it from the **process environment**, and an environment secret is not an env var | the job maps both secrets at job level (`env:`) |
| `Deployment … failed` + `common: - Bundle has content that does NOT have a .pom file: com/luxiangs/parsedock-client` | Maven **3.10** changes the archive layout handed to `central-publishing-maven-plugin`; the Portal's validator then sees content without a POM ([upstream #78](https://github.com/mavenplugins/central-publishing-maven-plugin/issues/78)) | the workflow installs Maven **3.9.9** from archive.apache.org with a pinned SHA-512 and prepends it to `PATH` |
| A tests classifier jar in the bundle | the generator attaches `test-jar`; consumers do not need our test classes | `scripts/patch_java_pom.py` removes that execution on every `make sdk` |
| `IllegalArgumentException: Unexpected char … in Content-Disposition value` for a CJK/Latin-1 file name | the `okhttp-gson` template interpolates `file.getName()` raw and OkHttp only accepts ASCII header values | `scripts/patch_java_client.py` rewrites the method to send an RFC 5987 `filename*` parameter on every `make sdk` (issue #66) |

The validation message from the Portal is printed in the workflow log as a `common:` block —
read it before changing anything, it names the exact path or file that failed.

## 4b. Verifying a published release

```bash
# Python (bypass a lagging mirror)
pip install --index-url https://pypi.org/simple parsedock==0.1.0
parsedock --version

# Java
curl -sI https://repo1.maven.org/maven2/com/luxiangs/parsedock-client/0.1.0/parsedock-client-0.1.0.pom
mvn -q dependency:get -Dartifact=com.luxiangs:parsedock-client:0.1.0
```

Then run the SDK suites against the published artifacts: `scripts/sdk-check.sh` installs
from the workspace, so point it at a fresh virtualenv (or the Maven coordinates) to prove
the released bytes, not just the checkout.

## 5. After the release

* `docs/CHANGELOG.md` gets a new section (the release notes are copied from it).
* `scripts/check_versions.py` must pass, which means the OpenAPI `info.version`,
  the binary, `pyproject.toml` and the generated POM were bumped together.
* Re-run `scripts/sdk-check.sh` and, for a service release, the acceptance suites
  in `docs/CI.md`.
