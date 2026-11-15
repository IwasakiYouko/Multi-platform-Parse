#!/usr/bin/env python3
"""Bundle exact Alpine corresponding sources for copyleft packages in an image.

Reads alpine-packages.tsv (binary, version, origin, commit, license) exported from
/lib/apk/db/installed, fetches the exact aports directory at each pinned commit and
upstream distfiles from Alpine's official distfiles mirrors, verifies every file's
SHA-512 against APKBUILD, and emits a deterministic source tarball + manifest.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import gzip
import hashlib
import io
import json
from pathlib import Path
import re
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


APORTS_ARCHIVE_BASE = "https://gitlab.alpinelinux.org/alpine/aports/-/archive"
DISTFILES_MIRRORS = (
    "https://distfiles.alpinelinux.org/distfiles/v3.24",
    "https://distfiles.alpinelinux.org/distfiles/edge",
)
PART_BYTES = 1024 * 1024 * 1024  # Safely below GitHub's 2 GiB per-asset limit.
COPYLEFT_RE = re.compile(r"\b(?:A?GPL|LGPL)\b", re.IGNORECASE)


def safe_name(name):
    if not name or Path(name).name != name or name in (".", "..") or "/" in name or "\\" in name:
        raise ValueError(f"Unsafe source filename: {name!r}")
    return name


def open_url(url, attempts=4, delay=2.0):
    if not url.startswith("https://"):
        raise ValueError(f"Refusing non-HTTPS source URL: {url}")
    for attempt in range(1, attempts + 1):
        try:
            response = urllib.request.urlopen(
                urllib.request.Request(url, headers={"User-Agent": "Multi-platform-Parse-alpine-sources/1"}),
                timeout=120,
            )
            if not response.url.startswith("https://"):
                response.close()
                raise ValueError(f"Source URL redirected outside HTTPS: {response.url}")
            return response
        except urllib.error.HTTPError as err:
            if err.code not in {408, 429, 500, 502, 503, 504} or attempt >= attempts:
                raise
        except (urllib.error.URLError, TimeoutError, ConnectionError) as err:
            if attempt >= attempts:
                raise err
        time.sleep(delay * attempt)


def parse_inventory(inventory_path):
    origins = {}
    for raw_line in inventory_path.read_text().splitlines():
        if not raw_line.strip():
            continue
        parts = raw_line.split("\t")
        if len(parts) != 5:
            raise ValueError(f"Malformed alpine-packages.tsv row: {raw_line!r}")
        binary, version, origin, commit, license_expr = (p.strip() for p in parts)
        if not COPYLEFT_RE.search(license_expr):
            continue
        origin = safe_name(origin)
        if not re.fullmatch(r"[0-9a-f]{40}", commit):
            raise ValueError(f"Invalid aports commit for {origin}: {commit!r}")
        entry = origins.setdefault(origin, {
            "origin": origin,
            "version": version,
            "commit": commit,
            "license": license_expr,
            "binaries": [],
        })
        if entry["commit"] != commit or entry["version"] != version:
            raise ValueError(f"Conflicting origin metadata for {origin}")
        entry["binaries"].append(binary)
    if not origins:
        raise ValueError("No copyleft packages found in inventory")
    for entry in origins.values():
        entry["binaries"].sort()
    return [origins[k] for k in sorted(origins)]


def parse_apkbuild_sha512sums(apkbuild_text):
    match = re.search(r'sha512sums="\s*(.*?)\s*"', apkbuild_text, re.DOTALL)
    if not match:
        raise ValueError("APKBUILD missing sha512sums")
    entries = []
    seen = set()
    for line in match.group(1).splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split(None, 1)
        if len(parts) != 2:
            raise ValueError(f"Malformed sha512sums entry: {line!r}")
        digest, fname = parts[0].lower(), safe_name(parts[1].strip())
        if not re.fullmatch(r"[0-9a-f]{128}", digest) or fname in seen:
            raise ValueError(f"Invalid or duplicate sha512sums entry for {fname}")
        seen.add(fname)
        entries.append((fname, digest))
    if not entries:
        raise ValueError("Empty sha512sums in APKBUILD")
    return entries


def fetch_aports_dir(origin, commit, dest_dir):
    for repo in ("main", "community"):
        url = f"{APORTS_ARCHIVE_BASE}/{commit}/aports-{commit}.tar.gz?path={repo}/{origin}"
        try:
            with open_url(url) as response:
                archive_bytes = response.read()
        except urllib.error.HTTPError as err:
            if err.code == 404:
                continue
            raise
        if not archive_bytes:
            continue
        extracted = {}
        with tarfile.open(fileobj=io.BytesIO(archive_bytes), mode="r:gz") as bundle:
            for member in bundle.getmembers():
                if not member.isfile():
                    continue
                fname = safe_name(Path(member.name).name)
                payload = bundle.extractfile(member).read()
                (dest_dir / fname).write_bytes(payload)
                extracted[fname] = payload
        if "APKBUILD" in extracted:
            return repo, url, extracted
    raise ValueError(f"Could not locate {origin} at commit {commit} in Alpine aports")


def download_distfile(fname, expected_sha512, destination):
    quoted = urllib.parse.quote(fname)
    last_err = None
    for mirror in DISTFILES_MIRRORS:
        url = f"{mirror}/{quoted}"
        try:
            sha512 = hashlib.sha512()
            sha256 = hashlib.sha256()
            size = 0
            with open_url(url) as response, destination.open("wb") as out:
                while chunk := response.read(1024 * 1024):
                    out.write(chunk)
                    size += len(chunk)
                    sha512.update(chunk)
                    sha256.update(chunk)
            if sha512.hexdigest() != expected_sha512:
                destination.unlink(missing_ok=True)
                raise ValueError(f"Distfile SHA-512 mismatch for {fname} from {url}")
            return {
                "name": fname,
                "source": url,
                "sha512": expected_sha512,
                "sha256": sha256.hexdigest(),
                "size": size,
            }
        except urllib.error.HTTPError as err:
            last_err = err
            if err.code == 404:
                continue
            raise
    raise ValueError(f"Distfile {fname} not found on Alpine mirrors") from last_err


def bundle_origin(entry, work_root):
    origin = entry["origin"]
    pkg_dir = work_root / f"{origin}-{entry['version']}"
    pkg_dir.mkdir(parents=True, exist_ok=True)
    repo, aports_url, local_files = fetch_aports_dir(origin, entry["commit"], pkg_dir)
    apkbuild_text = local_files["APKBUILD"].decode("utf-8", "replace")
    checksums = parse_apkbuild_sha512sums(apkbuild_text)

    verified_files = []
    # Include APKBUILD itself in manifest
    apkbuild_bytes = local_files["APKBUILD"]
    verified_files.append({
        "name": "APKBUILD",
        "source": aports_url,
        "sha512": hashlib.sha512(apkbuild_bytes).hexdigest(),
        "sha256": hashlib.sha256(apkbuild_bytes).hexdigest(),
        "size": len(apkbuild_bytes),
    })

    for fname, expected_sha512 in checksums:
        if fname in local_files:
            payload = local_files[fname]
            actual = hashlib.sha512(payload).hexdigest()
            if actual != expected_sha512:
                raise ValueError(f"Local aports file SHA-512 mismatch for {origin}/{fname}")
            verified_files.append({
                "name": fname,
                "source": aports_url,
                "sha512": expected_sha512,
                "sha256": hashlib.sha256(payload).hexdigest(),
                "size": len(payload),
            })
        else:
            meta = download_distfile(fname, expected_sha512, pkg_dir / fname)
            verified_files.append(meta)

    return {
        "origin": origin,
        "version": entry["version"],
        "commit": entry["commit"],
        "repository": repo,
        "license": entry["license"],
        "binaries": entry["binaries"],
        "aports_archive": aports_url,
        "files": verified_files,
    }


def write_archives(source_root, output_dir, release_tag, part_bytes=PART_BYTES):
    tar_path = source_root.parent / "sources.tar.gz"
    with tar_path.open("wb") as raw_out:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw_out, mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode="w") as tf:
                for path in sorted(source_root.rglob("*")):
                    if not path.is_file():
                        continue
                    rel = path.relative_to(source_root).as_posix()
                    info = tarfile.TarInfo(name=f"alpine-gpl-sources/{rel}")
                    info.size = path.stat().st_size
                    info.mode = 0o644
                    info.mtime = 0
                    info.uid = 0
                    info.gid = 0
                    info.uname = ""
                    info.gname = ""
                    with path.open("rb") as fh:
                        tf.addfile(info, fh)

    total_size = tar_path.stat().st_size
    base_name = f"multi-platform-parse-{release_tag}-alpine-gpl-sources.tar.gz"
    written = []
    if total_size <= part_bytes:
        target = output_dir / base_name
        target.write_bytes(tar_path.read_bytes())
        written.append(target)
    else:
        with tar_path.open("rb") as fh:
            idx = 1
            while chunk := fh.read(part_bytes):
                part = output_dir / f"{base_name}.part{idx:03d}"
                part.write_bytes(chunk)
                written.append(part)
                idx += 1
    return written


def build_bundle(inventory_path, output_dir, release_tag, workers=6):
    entries = parse_inventory(inventory_path)
    output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="alpine-sources-") as tmp:
        work_root = Path(tmp) / "tree"
        work_root.mkdir()
        with ThreadPoolExecutor(max_workers=workers) as pool:
            packages = list(pool.map(lambda item: bundle_origin(item, work_root), entries))
        archives = write_archives(work_root, output_dir, release_tag)

    manifest = {
        "release": release_tag,
        "package_count": len(packages),
        "archives": [
            {
                "name": path.name,
                "size": path.stat().st_size,
                "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
            }
            for path in archives
        ],
        "packages": packages,
    }
    manifest_path = output_dir / "alpine-source-manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    return manifest_path, archives


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", type=Path, required=True, help="path to alpine-packages.tsv")
    parser.add_argument("--output", type=Path, required=True, help="release output directory")
    parser.add_argument("--release", required=True, help="release tag such as v2.0.0")
    args = parser.parse_args()
    build_bundle(args.inventory, args.output, args.release)


if __name__ == "__main__":
    main()
