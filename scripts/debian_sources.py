#!/usr/bin/env python3
"""Bundle exact Debian source packages from an image's dpkg inventory.

Snapshot API: https://salsa.debian.org/snapshot-team/snapshot/raw/master/API
Only build/release tooling uses Python; deployed containers do not need it.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = "https://snapshot.debian.org"
PART_BYTES = 1024 * 1024 * 1024  # Safely below GitHub's 2 GiB per-asset limit.


def open_url(url):
    for attempt in range(4):
        try:
            return urllib.request.urlopen(
                urllib.request.Request(url, headers={"User-Agent": "Multi-platform-Parse-source-bundler/1"}),
                timeout=120,
            )
        except (urllib.error.URLError, TimeoutError):
            if attempt == 3:
                raise
            time.sleep(2 ** attempt)


def api(path):
    with open_url(BASE + path) as response:
        return json.load(response)


def safe_name(name):
    if not name or Path(name).name != name or name in (".", ".."):
        raise ValueError(f"Unsafe source filename: {name!r}")
    return name


def download(file_hash, destination, expected_size):
    if not re.fullmatch(r"[0-9a-f]{40}", file_hash):
        raise ValueError(f"Invalid snapshot SHA-1: {file_hash}")
    sha1 = hashlib.sha1()
    sha256 = hashlib.sha256()
    size = 0
    url = f"{BASE}/file/{file_hash}"
    with open_url(url) as response, destination.open("wb") as output:
        while chunk := response.read(1024 * 1024):
            output.write(chunk)
            size += len(chunk)
            sha1.update(chunk)
            sha256.update(chunk)
    if sha1.hexdigest() != file_hash or size != expected_size:
        raise ValueError(f"Snapshot checksum/size mismatch for {destination}")
    return {"name": destination.name, "url": url, "sha1": file_hash,
            "sha256": sha256.hexdigest(), "size": size}


def dsc_checksums(descriptor):
    checksums = {}
    in_checksums = False
    for line in descriptor.read_text().splitlines():
        if line == "Checksums-Sha256:":
            in_checksums = True
            continue
        if in_checksums and not line.startswith(" "):
            break
        if in_checksums:
            digest, size, name = line.split()
            name = safe_name(name)
            if name in checksums or not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise ValueError(f"Invalid or duplicate source checksum for {name}")
            checksums[name] = (digest, int(size))
    if not checksums:
        raise ValueError(f"Missing Checksums-Sha256 in {descriptor}")
    return checksums


def verify_dsc(directory, files):
    descriptors = list(directory.glob("*.dsc"))
    if len(descriptors) != 1:
        raise ValueError(f"Expected one Debian source descriptor in {directory}")
    by_name = {entry["name"]: entry for entry in files}
    for name, (digest, size) in dsc_checksums(descriptors[0]).items():
        entry = by_name[name]
        if digest != entry["sha256"] or size != entry["size"]:
            raise ValueError(f"Debian descriptor SHA-256 mismatch for {name}")


class SplitWriter:
    """Write one gzip stream across bounded, concatenatable Release assets."""

    def __init__(self, prefix, limit=PART_BYTES):
        self.prefix = prefix
        self.limit = limit
        self.part = 0
        self.position = 0
        self.output = None

    def write(self, data):
        size = len(data)
        remaining = memoryview(data)
        while remaining:
            if self.output is None or self.position == self.limit:
                self.close()
                self.part += 1
                self.output = Path(f"{self.prefix}.part{self.part:03d}").open("wb")
                self.position = 0
            count = min(len(remaining), self.limit - self.position)
            self.output.write(remaining[:count])
            self.position += count
            remaining = remaining[count:]
        return size

    def close(self):
        if self.output is not None:
            self.output.close()
            self.output = None


def bundle(inventory, destination, release):
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", release):
        raise ValueError("Release must be vMAJOR.MINOR.PATCH")
    packages = set()
    for line in inventory.read_text().splitlines():
        binary, binary_version, source, version = line.split("\t")
        if not all((binary, binary_version, source, version)):
            raise ValueError(f"Incomplete dpkg source inventory: {line!r}")
        packages.add((safe_name(source), safe_name(version)))
    if not packages:
        raise ValueError("Empty Debian source inventory")
    destination.mkdir(parents=True, exist_ok=True)
    manifest = {"release": release, "inventory": "debian-packages.tsv",
                "api_documentation": "https://salsa.debian.org/snapshot-team/snapshot/raw/master/API",
                "source_archives": ["debian", "debian-security"],
                "packages": []}
    prefix = destination / f"multi-platform-parse-{release}-debian-sources.tar.gz"
    writer = SplitWriter(prefix)
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        try:
            with tarfile.open(fileobj=writer, mode="w|gz", compresslevel=1) as archive:
                for source, version in sorted(packages):
                    print(f"Bundling {source} {version}", flush=True)
                    query = "/mr/package/{}/{}/srcfiles?fileinfo=1".format(
                        urllib.parse.quote(source, safe=""), urllib.parse.quote(version, safe=""))
                    metadata = api(query)
                    directory = root / source / version
                    directory.mkdir(parents=True)
                    by_name = {}
                    for entry in metadata["result"]:
                        file_hash = entry["hash"]
                        for info in metadata["fileinfo"][file_hash]:
                            if info["archive_name"] not in manifest["source_archives"]:
                                continue
                            name = safe_name(info["name"])
                            by_name.setdefault(name, set()).add((file_hash, int(info["size"])))
                    descriptors = [name for name in by_name if name.endswith(".dsc")]
                    if len(descriptors) != 1:
                        raise ValueError(f"Expected one source descriptor for {source} {version}")

                    def fetch(name):
                        candidates = by_name.get(name, set())
                        if len(candidates) != 1:
                            raise ValueError(f"Missing or ambiguous source filename: {name}")
                        file_hash, size = next(iter(candidates))
                        return download(file_hash, directory / name, size)

                    descriptor = descriptors[0]
                    files = [fetch(descriptor)]
                    for name in dsc_checksums(directory / descriptor):
                        files.append(fetch(name))
                    verify_dsc(directory, files)
                    manifest["packages"].append({"name": source, "version": version,
                                                  "api": BASE + query, "files": files})
                    for path in sorted(directory.iterdir()):
                        archive.add(path, arcname=f"sources/{source}/{version}/{path.name}", recursive=False)
                    shutil.rmtree(directory)
                manifest_path = destination / "debian-source-manifest.json"
                manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
                archive.add(manifest_path, arcname=manifest_path.name, recursive=False)
                archive.add(inventory, arcname="debian-packages.tsv", recursive=False)
        finally:
            writer.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--release", required=True)
    args = parser.parse_args()
    bundle(args.inventory, args.output, args.release)
