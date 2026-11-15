import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import alpine_sources


class Response(io.BytesIO):
    def __init__(self, payload, url="https://distfiles.alpinelinux.org/distfiles/v3.24/demo-1.0.tar.xz"):
        super().__init__(payload)
        self.url = url

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.close()


class AlpineSourcesTests(unittest.TestCase):
    def test_parse_inventory_selects_only_copyleft_and_deduplicates_origin(self):
        commit = "a" * 40
        with tempfile.TemporaryDirectory() as tmp:
            inv = Path(tmp) / "alpine-packages.tsv"
            inv.write_text(
                f"ffmpeg\t8.1.3-r0\tffmpeg\t{commit}\tGPL-2.0-or-later AND LGPL-2.1-or-later\n"
                f"ffmpeg-libavcodec\t8.1.3-r0\tffmpeg\t{commit}\tGPL-2.0-or-later AND LGPL-2.1-or-later\n"
                f"deno\t2.7.4-r2\tdeno\t{'b' * 40}\tMIT\n"
            )
            entries = alpine_sources.parse_inventory(inv)
            self.assertEqual(len(entries), 1)
            self.assertEqual(entries[0]["origin"], "ffmpeg")
            self.assertEqual(entries[0]["binaries"], ["ffmpeg", "ffmpeg-libavcodec"])

    def test_parse_apkbuild_sha512sums_rejects_unsafe_paths_and_bad_hashes(self):
        good_hash = "ab" * 64
        parsed = alpine_sources.parse_apkbuild_sha512sums(
            f'pkgname=demo\nsha512sums="\n{good_hash}  demo-1.0.tar.xz\n"\n'
        )
        self.assertEqual(parsed, [("demo-1.0.tar.xz", good_hash)])
        with self.assertRaisesRegex(ValueError, "Unsafe source filename"):
            alpine_sources.parse_apkbuild_sha512sums(f'sha512sums="{good_hash}  ../escape.tar.xz"')
        with self.assertRaisesRegex(ValueError, "Invalid or duplicate"):
            alpine_sources.parse_apkbuild_sha512sums('sha512sums="1234  demo.tar.xz"')

    def test_build_bundle_verifies_aports_and_distfiles_sha512(self):
        commit = "c" * 40
        dist_payload = b"upstream-gpl-tarball-bytes"
        patch_payload = b"--- a/main.c\n+++ b/main.c\n"
        dist_sha512 = hashlib.sha512(dist_payload).hexdigest()
        patch_sha512 = hashlib.sha512(patch_payload).hexdigest()
        apkbuild_payload = (
            "pkgname=demo\npkgver=1.0\n"
            f'sha512sums="\n{dist_sha512}  demo-1.0.tar.xz\n{patch_sha512}  fix.patch\n"\n'
        ).encode()

        aports_buf = io.BytesIO()
        with tarfile.open(fileobj=aports_buf, mode="w:gz") as tf:
            for name, payload in (("APKBUILD", apkbuild_payload), ("fix.patch", patch_payload)):
                info = tarfile.TarInfo(name=f"aports-{commit}/main/demo/{name}")
                info.size = len(payload)
                tf.addfile(info, io.BytesIO(payload))
        aports_bytes = aports_buf.getvalue()

        def fake_open_url(url, *args, **kwargs):
            if "aports-" in url:
                return Response(aports_bytes, url=url)
            if url.endswith("/demo-1.0.tar.xz"):
                return Response(dist_payload, url=url)
            raise AssertionError(f"Unexpected URL: {url}")

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            inv = root / "alpine-packages.tsv"
            inv.write_text(f"demo\t1.0-r0\tdemo\t{commit}\tGPL-2.0-only\n")
            out = root / "release"
            with patch.object(alpine_sources, "open_url", side_effect=fake_open_url):
                manifest_path, archives = alpine_sources.build_bundle(inv, out, "v2.0.0", workers=1)
            self.assertEqual(len(archives), 1)
            manifest = json.loads(manifest_path.read_text())
            self.assertEqual(manifest["package_count"], 1)
            with tarfile.open(archives[0], "r:gz") as tf:
                names = set(tf.getnames())
                self.assertIn("alpine-gpl-sources/demo-1.0-r0/APKBUILD", names)
                self.assertIn("alpine-gpl-sources/demo-1.0-r0/fix.patch", names)
                self.assertIn("alpine-gpl-sources/demo-1.0-r0/demo-1.0.tar.xz", names)


if __name__ == "__main__":
    unittest.main()
