import hashlib
import io
import json
from pathlib import Path
import stat
import tarfile
import tempfile
import tomllib
import unittest
from unittest.mock import patch
import zipfile

import install_ytdlp_runtime as runtime


class Response(io.BytesIO):
    url = "https://example.invalid/artifact"


class RuntimePackagingTests(unittest.TestCase):
    def test_bundled_deno_notices_cover_pinned_lock_and_native_runtime(self):
        lock = json.loads(runtime.LOCK.read_text())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            runtime.copy_deno_notices(lock["deno_notices"], root)
            with tarfile.open(root / lock["deno_notices"]["filename"], "r:gz") as bundle:
                provenance = json.load(bundle.extractfile("PROVENANCE.json"))
                cargo_lock = tomllib.loads(bundle.extractfile("Deno-Cargo.lock").read().decode())
                expected = {(p["name"], p["version"], p["checksum"])
                            for p in cargo_lock["package"] if "checksum" in p}
                actual = {(p["name"], p["version"], p["sha256"])
                          for p in provenance["registry_packages"]}
                self.assertEqual(actual, expected)
                for path in (
                    "native/v8/LICENSE",
                    "native/third_party/libc++/src/LICENSE.TXT",
                    "native/rust-1.95.0/LICENSE-MIT",
                ):
                    self.assertGreater(bundle.getmember(path).size, 100)
            changed = dict(lock["deno_notices"], sha256="0" * 64)
            with self.assertRaisesRegex(ValueError, "notices checksum/size"):
                runtime.copy_deno_notices(changed, root)

    def test_wheel_selection_requires_one_compatible_variant_per_package(self):
        artifacts = [
            {"name": "brotli", "python": "3.11"},
            {"name": "brotli", "python": "3.12"},
            {"name": "requests"},
        ]
        self.assertEqual(runtime.select_wheels(artifacts, "3.11"), [artifacts[0], artifacts[2]])
        self.assertEqual(runtime.select_wheels(artifacts, "3.12"), [artifacts[1], artifacts[2]])
        with self.assertRaisesRegex(ValueError, "no unique locked"):
            runtime.select_wheels(artifacts, "3.15")
        with self.assertRaisesRegex(ValueError, "no unique locked"):
            runtime.select_wheels(artifacts + [artifacts[0]], "3.11")

    def test_download_requires_both_locked_size_and_hash(self):
        payload = b"locked runtime"
        artifact = {"url": Response.url, "size": len(payload),
                    "sha256": hashlib.sha256(payload).hexdigest()}
        with tempfile.TemporaryDirectory() as temporary:
            destination = Path(temporary) / "artifact"
            with patch.object(runtime.urllib.request, "urlopen", return_value=Response(payload)):
                runtime.download(artifact, destination)
            self.assertEqual(destination.read_bytes(), payload)
            for changed in (b"changed input!", payload[:-1], payload + b"!"):
                with self.subTest(payload=changed), patch.object(
                    runtime.urllib.request, "urlopen", return_value=Response(changed)
                ):
                    with self.assertRaisesRegex(ValueError, "locked size|checksum/size"):
                        runtime.download(artifact, destination)

    def test_download_rejects_https_downgrade(self):
        response = Response(b"a")
        response.url = "http://example.invalid/artifact"
        artifact = {"url": Response.url, "size": 1, "sha256": hashlib.sha256(b"a").hexdigest()}
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            runtime.urllib.request, "urlopen", return_value=response
        ):
            with self.assertRaisesRegex(ValueError, "outside HTTPS"):
                runtime.download(artifact, Path(temporary) / "artifact")

    def test_download_retries_transient_gateway_timeout(self):
        """Verify transient HTTP 504 gateway timeouts are retried and verified."""
        payload = b"recovered"
        artifact = {"url": Response.url, "size": len(payload),
                    "sha256": hashlib.sha256(payload).hexdigest()}
        gateway_timeout = runtime.urllib.error.HTTPError(
            Response.url, 504, "Gateway Time-out", hdrs=None, fp=None
        )
        with tempfile.TemporaryDirectory() as temporary:
            destination = Path(temporary) / "artifact"
            with patch.object(
                runtime.urllib.request,
                "urlopen",
                side_effect=[gateway_timeout, Response(payload)],
            ) as mocked:
                runtime.download(artifact, destination, attempts=2, delay=0)
            self.assertEqual(mocked.call_count, 2)
            self.assertEqual(destination.read_bytes(), payload)

    def test_deno_rejects_symlink_and_unexpected_members(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for members in (["deno", "../escape"], ["deno"]):
                archive = root / "runtime.zip"
                with zipfile.ZipFile(archive, "w") as package:
                    for name in members:
                        member = zipfile.ZipInfo(name)
                        member.external_attr = (stat.S_IFLNK | 0o777) << 16
                        package.writestr(member, "/bin/sh")
                with self.assertRaisesRegex(ValueError, "unexpected|invalid"):
                    runtime.install_deno(archive, root / "bin/deno")
                self.assertFalse((root / "bin/deno").exists())

    def test_wheel_licenses_include_embedded_js_dependency_notices(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wheel = root / "ejs.whl"
            files = {
                "ejs.dist-info/licenses/": b"",
                "crypto.dist-info/LICENSE.rst": b"BSD and public domain\n",
                "crypto.dist-info/AUTHORS.rst": b"Upstream authors\n",
                "ejs.dist-info/licenses/LICENSE": b"Unlicense\n",
                "ejs.dist-info/METADATA": b"Name: ejs\nVersion: 0.8.0\n",
                "yt_dlp_ejs/yt/solver/lib.min.js": b"/*! ISC meriyah; MIT astring */\nvar lib = {};",
            }
            with zipfile.ZipFile(wheel, "w") as package:
                for name, payload in files.items():
                    package.writestr(name, payload)
            output = root / "licenses"
            runtime.collect_wheel_licenses(wheel, output)
            self.assertEqual((output / "ejs.dist-info/licenses/LICENSE").read_bytes(), b"Unlicense\n")
            self.assertEqual((output / "lib.min.js.LICENSE").read_bytes(), b"/*! ISC meriyah; MIT astring */\n")
            self.assertEqual((output / "ejs.dist-info/METADATA").read_bytes(), files["ejs.dist-info/METADATA"])
            self.assertEqual((output / "crypto.dist-info/LICENSE.rst").read_bytes(), b"BSD and public domain\n")
            self.assertEqual((output / "crypto.dist-info/AUTHORS.rst").read_bytes(), b"Upstream authors\n")

    def test_wheel_license_paths_cannot_escape_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            wheel = root / "bad.whl"
            with zipfile.ZipFile(wheel, "w") as package:
                package.writestr("../bad.dist-info/licenses/LICENSE", b"bad")
            with self.assertRaisesRegex(ValueError, "unsafe wheel member"):
                runtime.collect_wheel_licenses(wheel, root / "licenses")
            self.assertFalse((root / "bad.dist-info").exists())


if __name__ == "__main__":
    unittest.main()
