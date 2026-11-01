#!/usr/bin/env python3
"""Make the generated Java client upload file names OkHttp and RFC 2231 accept.

`sdk/java-generated` comes from OpenAPI Generator's `okhttp-gson` template, whose
`ApiClient.addPartToMultiPartBuilder(... File file)` interpolates `file.getName()`
into the `Content-Disposition` header verbatim. OkHttp's `Headers` only accepts
`0x20`-`0x7e`, so any non-ASCII name (Chinese, Portuguese, Cyrillic, ...) throws an
`IllegalArgumentException` before a request is built; a `"` passes the check but
ends the parameter value, so the server sees no usable file part (issue #66).

The replacement emits RFC 6266 with an RFC 5987 extended parameter
(`filename*=UTF-8''...`) and keeps the plain form for names that are already safe.
Go's `mime.ParseMediaType` in the coordinator prefers the extended value.

This script is idempotent and runs right after generation:

    python3 scripts/patch_java_client.py sdk/java-generated/src/main/java/org/openapitools/client/ApiClient.java

It rewrites exactly one method and exits non-zero when the anchor disappears, so a
generator bump cannot silently reintroduce the bug.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ANCHOR = (
    "protected void addPartToMultiPartBuilder("
    "MultipartBody.Builder mpBuilder, String key, File file) {"
)

# The generated body is a handful of statements and contains no nested braces, so
# the first indentation-aligned closing brace belongs to this method.
METHOD = re.compile(
    r"(?P<indent>[ \t]*)"
    + re.escape(ANCHOR)
    + r"\n.*?\n(?P=indent)\}\n",
    re.S,
)

PATCHED = """    protected void addPartToMultiPartBuilder(MultipartBody.Builder mpBuilder, String key, File file) {
        String name = file.getName();
        // OkHttp's Headers only accepts 0x20-0x7e and a '"' would end the parameter, so
        // non-plain names go out as RFC 6266 with an RFC 5987 extended parameter: an
        // ASCII fallback plus filename*=UTF-8''. The coordinator's mime.ParseMediaType
        // prefers the extended value (issue #66).
        String disposition;
        if (isPlainAsciiFilename(name)) {
            disposition = "form-data; name=\\"" + key + "\\"; filename=\\"" + name + "\\"";
        } else {
            disposition = "form-data; name=\\"" + key + "\\"; filename=\\"" + asciiFallback(name)
                    + "\\"; filename*=UTF-8''" + encodeRFC5987(name);
        }
        Headers partHeaders = Headers.of("Content-Disposition", disposition);
        MediaType mediaType = MediaType.parse(guessContentTypeFromFile(file));
        mpBuilder.addPart(partHeaders, RequestBody.create(file, mediaType));
    }

    /**
     * True when a file name can be placed verbatim in a quoted Content-Disposition
     * parameter: printable ASCII only (OkHttp's Headers.checkValue) and no double
     * quote or backslash (either would change the parameter's meaning).
     */
    private static boolean isPlainAsciiFilename(String name) {
        for (int i = 0; i < name.length(); i++) {
            char c = name.charAt(i);
            if (c < 0x20 || c > 0x7e || c == '"' || c == '\\\\') {
                return false;
            }
        }
        return true;
    }

    /** ASCII stand-in for the plain filename parameter, for parsers that ignore filename*. */
    private static String asciiFallback(String name) {
        StringBuilder out = new StringBuilder(name.length());
        for (int i = 0; i < name.length(); i++) {
            char c = name.charAt(i);
            out.append(c >= 0x20 && c <= 0x7e && c != '"' && c != '\\\\' ? c : '_');
        }
        return out.toString();
    }

    /** RFC 5987 attr-char percent-encoding of the UTF-8 form of {@code value}. */
    private static String encodeRFC5987(String value) {
        StringBuilder out = new StringBuilder(value.length() * 3);
        for (byte b : value.getBytes(java.nio.charset.StandardCharsets.UTF_8)) {
            int c = b & 0xff;
            if ((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
                    || "!#$&+-.^_`|~".indexOf(c) >= 0) {
                out.append((char) c);
            } else {
                out.append('%')
                        .append(Character.toUpperCase(Character.forDigit(c >> 4, 16)))
                        .append(Character.toUpperCase(Character.forDigit(c & 0xf, 16)));
            }
        }
        return out.toString();
    }
"""

MARKER = "filename*=UTF-8''"


def patch(path: Path) -> bool:
    text = path.read_text(encoding="utf-8")
    match = METHOD.search(text)
    if match:
        method = match.group(0)
        if MARKER in method:
            # Still patched, but make sure a partial edit (method rewritten without the
            # helpers) cannot pass as current.
            if all(helper in text for helper in
                   ("isPlainAsciiFilename", "asciiFallback", "encodeRFC5987")):
                return False
    elif MARKER in text:
        # The anchor changed but the file already carries our encoding: a generator
        # bump rewrote the method around a marker we no longer understand.
        raise SystemExit(
            f"{path}: found {MARKER} but not the method anchor; "
            "update scripts/patch_java_client.py for the new generator output"
        )
    else:
        raise SystemExit(
            f"{path}: could not find {ANCHOR!r}; "
            "update scripts/patch_java_client.py for the new generator output"
        )

    text = text[:match.start()] + PATCHED + text[match.end():]
    path.write_text(text, encoding="utf-8")
    return True


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: patch_java_client.py <ApiClient.java>")
    path = Path(sys.argv[1])
    if not path.is_file():
        raise SystemExit(f"no such file: {path}")
    print(f"{path}: {'patched' if patch(path) else 'already current'}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
