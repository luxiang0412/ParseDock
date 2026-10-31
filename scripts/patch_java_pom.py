#!/usr/bin/env python3
"""Give the generated Java client the metadata and release profile Maven Central requires.

`sdk/java-generated` is produced by OpenAPI Generator (`make sdk`) and starts with
placeholder metadata: "OpenAPI Java" as the description, the generator's own SCM
URLs, and `Unlicense` as the licence (with an Apache URL, no less). Central
rejects a release for missing name/description/url/licences/developers/scm, for a
missing sources or javadoc jar, and for unsigned artifacts.

This script is idempotent and runs right after generation:

    python3 scripts/patch_java_pom.py sdk/java-generated/pom.xml

It only rewrites the metadata block and appends a `release` profile, so ordinary
`mvn package` (CI, tests) needs no GPG key and stays fast; publishing uses
`mvn -Prelease deploy`.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = "https://github.com/luxiang0412/ParseDock"
PROJECT_URL = REPO
# Placeholder values the generator always writes, used to detect an unpatched POM.
PLACEHOLDER = "OpenAPI Java"

METADATA = f"""    <url>{PROJECT_URL}</url>
    <description>ParseDock API client for Java: upload documents, poll jobs, download Markdown or the Document IR, and release storage explicitly (ACK).</description>
    <scm>
        <connection>scm:git:{REPO}.git</connection>
        <developerConnection>scm:git:git@github.com:luxiang0412/ParseDock.git</developerConnection>
        <url>{REPO}</url>
        <tag>v0.1.0</tag>
    </scm>

    <licenses>
        <license>
            <name>Apache License 2.0</name>
            <url>https://www.apache.org/licenses/LICENSE-2.0.txt</url>
            <distribution>repo</distribution>
        </license>
    </licenses>

    <developers>
        <developer>
            <id>luxiang0412</id>
            <name>ParseDock maintainers</name>
            <url>{REPO}</url>
        </developer>
    </developers>
"""

RELEASE_PROFILE = """
    <profiles>
        <profile>
            <!-- Publishing only: `mvn -Prelease deploy`. Kept out of the default
                 build so tests need no signing key. -->
            <id>release</id>
            <build>
                <plugins>
                    <plugin>
                        <groupId>org.apache.maven.plugins</groupId>
                        <artifactId>maven-source-plugin</artifactId>
                        <version>3.3.1</version>
                        <executions>
                            <execution>
                                <id>attach-sources</id>
                                <goals><goal>jar-no-fork</goal></goals>
                            </execution>
                        </executions>
                    </plugin>
                    <plugin>
                        <groupId>org.apache.maven.plugins</groupId>
                        <artifactId>maven-javadoc-plugin</artifactId>
                        <version>3.10.1</version>
                        <configuration>
                            <doclint>none</doclint>
                            <quiet>true</quiet>
                        </configuration>
                        <executions>
                            <execution>
                                <id>attach-javadocs</id>
                                <goals><goal>jar</goal></goals>
                            </execution>
                        </executions>
                    </plugin>
                    <plugin>
                        <groupId>org.apache.maven.plugins</groupId>
                        <artifactId>maven-gpg-plugin</artifactId>
                        <version>3.2.7</version>
                        <configuration>
                            <passphraseEnvName>MAVEN_GPG_PASSPHRASE</passphraseEnvName>
                        </configuration>
                        <executions>
                            <execution>
                                <id>sign-artifacts</id>
                                <phase>verify</phase>
                                <goals><goal>sign</goal></goals>
                            </execution>
                        </executions>
                    </plugin>
                    <plugin>
                        <groupId>org.sonatype.central</groupId>
                        <artifactId>central-publishing-maven-plugin</artifactId>
                        <version>0.8.0</version>
                        <extensions>true</extensions>
                        <configuration>
                            <publishingServerId>central</publishingServerId>
                            <autoPublish>false</autoPublish>
                        </configuration>
                    </plugin>
                </plugins>
            </build>
        </profile>
    </profiles>
"""


def patch(path: Path) -> bool:
    text = path.read_text(encoding="utf-8")
    changed = False

    if PLACEHOLDER in text:
        # Replace everything from <url> through </developers>, the block the
        # generator fills with its own project's metadata.
        pattern = re.compile(r"[ \t]*<url>.*?</developers>\n", re.S)
        text, n = pattern.subn(METADATA, text, count=1)
        if n != 1:
            raise SystemExit(f"{path}: could not find the metadata block to replace")
        changed = True

    if "<id>release</id>" not in text:
        # The generator emits its own <profiles> with a bare GPG sign profile.
        # That block is replaced wholesale: signing belongs with the sources,
        # javadoc and publishing steps so they cannot be run half-way.
        existing = re.compile(r"[ \t]*<profiles>.*?</profiles>\n", re.S)
        if existing.search(text):
            text, n = existing.subn(RELEASE_PROFILE, text, count=1)
            if n != 1:
                raise SystemExit(f"{path}: could not replace the generated profiles block")
        else:
            text = text.replace("</project>", RELEASE_PROFILE + "</project>")
        changed = True

    if changed:
        path.write_text(text, encoding="utf-8")
    return changed


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: patch_java_pom.py <pom.xml>")
    path = Path(sys.argv[1])
    if not path.is_file():
        raise SystemExit(f"no such file: {path}")
    print(f"{path}: {'patched' if patch(path) else 'already current'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
