#!/usr/bin/env python3
"""File-name encoding in both SDKs (issue #66).

A name that is not plain printable ASCII must not reach a header verbatim: OkHttp
rejects it, and a ``"`` ends the Content-Disposition parameter so the server sees
no file part. Both clients send an RFC 5987 ``filename*`` parameter instead, and
the generated Java client is patched to do so after every regeneration.
"""
from __future__ import annotations

import importlib.util
import re
import sys
import tempfile
import unittest
import urllib.parse
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "sdk" / "python"))

from parsedock_sdk import ParseDockClient  # noqa: E402

GENERATED = ROOT / "sdk" / "java-generated" / "src" / "main" / "java" / "org" / "openapitools" / "client" / "ApiClient.java"
PATCH = ROOT / "scripts" / "patch_java_client.py"


def load_patch_module():
    spec = importlib.util.spec_from_file_location("patch_java_client", PATCH)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def file_part_header(name: str) -> bytes:
    """The raw Content-Disposition line the Python SDK sends for ``name``."""
    with tempfile.TemporaryDirectory(prefix="sdk-names-") as tmp:
        path = Path(tmp) / name
        path.write_bytes(b"%PDF-1.4\n")
        body, _ = ParseDockClient._multipart({"mode": "fast"}, str(path))
    for line in body.split(b"\r\n"):
        if line.startswith(b"Content-Disposition: form-data; name=\"file\""):
            return line
    raise AssertionError("file part has no Content-Disposition header")


class PythonMultipartTest(unittest.TestCase):
    def test_plain_name_is_unchanged(self):
        header = file_part_header("plain name.pdf")
        self.assertIn(b'filename="plain name.pdf"', header)
        self.assertNotIn(b"filename*", header)

    def test_non_ascii_name_uses_extended_parameter(self):
        header = file_part_header("测试-合同.pdf")
        # The header itself must be plain ASCII, or an OkHttp-style client refuses it.
        text = header.decode("ascii")
        match = re.search(r"filename\*=UTF-8''([^;\r\n]+)", text)
        self.assertIsNotNone(match, text)
        self.assertEqual(urllib.parse.unquote(match.group(1)), "测试-合同.pdf")
        self.assertIn('filename="__-__.pdf"', text)

    def test_quote_and_backslash_cannot_end_the_parameter(self):
        for name, fallback in (('a"b.pdf', "a_b.pdf"), ("a\\b.pdf", "a_b.pdf")):
            text = file_part_header(name).decode("ascii")
            match = re.search(r"filename\*=UTF-8''([^;\r\n]+)", text)
            self.assertIsNotNone(match, text)
            self.assertEqual(urllib.parse.unquote(match.group(1)), name)
            self.assertIn(f'filename="{fallback}"', text)


class JavaClientPatchTest(unittest.TestCase):
    def test_generated_client_is_patched(self):
        text = GENERATED.read_text(encoding="utf-8")
        self.assertIn("filename*=UTF-8''", text)
        self.assertIn("encodeRFC5987", text)
        # The stock template interpolates the raw name; that is the bug.
        self.assertNotIn('filename=\\"" + file.getName()', text)

    def test_patch_is_idempotent(self):
        module = load_patch_module()
        with tempfile.TemporaryDirectory(prefix="java-client-patch-") as tmp:
            source = Path(tmp) / "ApiClient.java"
            source.write_text(
                "class ApiClient {\n"
                "    protected void addPartToMultiPartBuilder("
                "MultipartBody.Builder mpBuilder, String key, File file) {\n"
                '        Headers partHeaders = Headers.of("Content-Disposition", '
                '"form-data; name=\\"" + key + "\\"; filename=\\"" + file.getName() + "\\"");\n'
                "        mpBuilder.addPart(partHeaders, RequestBody.create(file, null));\n"
                "    }\n"
                "}\n",
                encoding="utf-8",
            )
            self.assertTrue(module.patch(source))
            first = source.read_text(encoding="utf-8")
            self.assertIn("filename*=UTF-8''", first)
            self.assertFalse(module.patch(source))
            self.assertEqual(source.read_text(encoding="utf-8"), first)

    def test_missing_anchor_fails_loudly(self):
        module = load_patch_module()
        with tempfile.TemporaryDirectory(prefix="java-client-patch-") as tmp:
            source = Path(tmp) / "ApiClient.java"
            source.write_text("class ApiClient {}\n", encoding="utf-8")
            with self.assertRaises(SystemExit):
                module.patch(source)


if __name__ == "__main__":
    unittest.main()
