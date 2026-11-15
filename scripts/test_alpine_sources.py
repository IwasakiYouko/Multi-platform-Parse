import hashlib
import io
import json
from pathlib import Path
import subprocess
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

    def test_parse_cat_file_batch_handles_binary_blobs_with_newlines(self):
        sha1 = "1" * 40
        sha2 = "2" * 40
        p1 = b"line1\nline2\x00\n"
        p2 = b"APKBUILD"
        stream = (
            f"{sha1} blob {len(p1)}\n".encode() + p1 + b"\n"
            + f"{sha2} blob {len(p2)}\n".encode() + p2 + b"\n"
        )
        parsed = alpine_sources.parse_cat_file_batch(stream, [sha1, sha2])
        self.assertEqual(parsed, {sha1: p1, sha2: p2})

    def test_prefetch_aports_dirs_fetches_from_local_git_remote_when_patched(self):
        dist_payload = b"upstream-gpl-tarball-bytes"
        patch_payload = b"--- a/main.c\n+++ b/main.c\n"
        dist_sha512 = hashlib.sha512(dist_payload).hexdigest()
        patch_sha512 = hashlib.sha512(patch_payload).hexdigest()
        apkbuild_payload = (
            "pkgname=demo\npkgver=1.0\n"
            f'sha512sums="\n{dist_sha512}  demo-1.0.tar.xz\n{patch_sha512}  fix.patch\n"\n'
        ).encode()

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            origin_repo = root / "origin-aports"
            pkg_src = origin_repo / "main" / "demo"
            pkg_src.mkdir(parents=True)
            (pkg_src / "APKBUILD").write_bytes(apkbuild_payload)
            (pkg_src / "fix.patch").write_bytes(patch_payload)
            subprocess.run(["git", "init", "-q", str(origin_repo)], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "config", "user.email", "test@example.com"], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "config", "user.name", "Test"], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "config", "uploadpack.allowFilter", "true"], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "config", "uploadpack.allowAnySHA1InWant", "true"], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "add", "."], check=True)
            subprocess.run(["git", "-C", str(origin_repo), "commit", "-q", "-m", "init"], check=True)
            commit = subprocess.check_output(["git", "-C", str(origin_repo), "rev-parse", "HEAD"], text=True).strip()

            def fake_run_git_fetch(repo_dir, remote, objects, extra_args=(), attempts=3, delay=2.0):
                subprocess.run(
                    ["git", "-C", str(repo_dir), "fetch", "--quiet", "--no-write-fetch-head", *extra_args, f"file://{origin_repo}", *objects],
                    check=True,
                )

            def fake_open_url(url, *args, **kwargs):
                if url.endswith("/demo-1.0.tar.xz"):
                    return Response(dist_payload, url=url)
                raise AssertionError(f"Unexpected URL: {url}")

            inv = root / "alpine-packages.tsv"
            inv.write_text(f"demo\t1.0-r0\tdemo\t{commit}\tGPL-2.0-only\n")
            out = root / "release"
            with patch.object(alpine_sources, "run_git_fetch", side_effect=fake_run_git_fetch), \
                 patch.object(alpine_sources, "open_url", side_effect=fake_open_url):
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
