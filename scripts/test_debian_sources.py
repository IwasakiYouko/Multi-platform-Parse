import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import debian_sources as sources


class SourceBundleTests(unittest.TestCase):
    def test_descriptor_names_survive_snapshot_version_aliases(self):
        payload = b"corresponding upstream source\n"
        name = "example_1.orig.tar.gz"
        descriptor = ("Format: 3.0 (quilt)\nChecksums-Sha256:\n "
                      + hashlib.sha256(payload).hexdigest()
                      + f" {len(payload)} {name}\n").encode()
        files = {name: payload, "example_1-1.dsc": descriptor}
        by_hash = {hashlib.sha1(data).hexdigest(): data for data in files.values()}
        metadata = {
            "result": [{"hash": digest} for digest in by_hash],
            "fileinfo": {hashlib.sha1(data).hexdigest(): [{"name": filename, "size": len(data), "archive_name": "debian"}]
                         for filename, data in files.items()},
        }
        metadata["fileinfo"][hashlib.sha1(payload).hexdigest()].insert(
            0, {"name": "example_0.orig.tar.gz", "size": len(payload), "archive_name": "debian"})
        ports_descriptor = descriptor + b"Build-Architecture: hurd-amd64\n"
        ports_hash = hashlib.sha1(ports_descriptor).hexdigest()
        by_hash[ports_hash] = ports_descriptor
        metadata["result"].append({"hash": ports_hash})
        metadata["fileinfo"][ports_hash] = [{
            "name": "example_1-1.dsc", "size": len(ports_descriptor), "archive_name": "debian-ports"}]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            inventory = root / "debian-packages.tsv"
            inventory.write_text("example-bin\t1-1+b1\texample\t1-1\n")
            output = root / "release"
            with patch.object(sources, "api", return_value=metadata), patch.object(
                sources, "open_url", side_effect=lambda url: io.BytesIO(by_hash[url.rsplit("/", 1)[1]])
            ):
                sources.bundle(inventory, output, "v1.0.0")
            parts = sorted(output.glob("*.part*"))
            with tarfile.open(fileobj=io.BytesIO(b"".join(path.read_bytes() for path in parts)), mode="r:gz") as archive:
                self.assertEqual(archive.extractfile("sources/example/1-1/" + name).read(), payload)
                self.assertEqual(archive.extractfile("sources/example/1-1/example_1-1.dsc").read(), descriptor)
                self.assertIn("debian-packages.tsv", archive.getnames())
            manifest = json.loads((output / "debian-source-manifest.json").read_text())
            self.assertEqual(manifest["packages"][0]["version"], "1-1")

    def test_split_writer_preserves_exact_stream(self):
        with tempfile.TemporaryDirectory() as temporary:
            prefix = Path(temporary) / "sources.tar.gz"
            writer = sources.SplitWriter(prefix, limit=4)
            writer.write(b"abcdefghij")
            writer.close()
            parts = sorted(prefix.parent.glob("*.part*"))
            self.assertEqual([path.stat().st_size for path in parts], [4, 4, 2])
            self.assertEqual(b"".join(path.read_bytes() for path in parts), b"abcdefghij")

    def test_download_rejects_wrong_hash(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            sources, "open_url", return_value=io.BytesIO(b"changed")
        ):
            with self.assertRaisesRegex(ValueError, "checksum/size mismatch"):
                sources.download("0" * 40, Path(temporary) / "source.tar.xz", 7)

    def test_descriptor_rejects_wrong_sha256(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "source.dsc").write_text("Checksums-Sha256:\n " + "0" * 64 + " 1 source.tar.xz\n")
            with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
                sources.verify_dsc(root, [{"name": "source.tar.xz", "sha256": "1" * 64, "size": 1}])


if __name__ == "__main__":
    unittest.main()
