#!/usr/bin/env python3
"""Bundle exact Alpine corresponding sources for copyleft packages in an image.

Reads alpine-packages.tsv (binary, version, origin, commit, license) exported from
/lib/apk/db/installed, fetches the exact aports directory at each pinned commit via
Git smart HTTP (primary: github.com/alpinelinux/aports.git, fallback:
gitlab.alpinelinux.org/alpine/aports.git) plus upstream distfiles from Alpine's
official distfiles mirrors, verifies every file's SHA-512 against APKBUILD, and
emits a deterministic source tarball + manifest.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import gzip
import hashlib
import json
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


APORTS_GIT_REMOTES = (
    "https://github.com/alpinelinux/aports.git",
    "https://gitlab.alpinelinux.org/alpine/aports.git",
)
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


def parse_cat_file_batch(raw_bytes, expected_shas):
    blobs = {}
    offset = 0
    total = len(raw_bytes)
    for expected in expected_shas:
        nl = raw_bytes.find(b"\n", offset)
        if nl < 0:
            raise ValueError(f"Truncated git cat-file header for {expected}")
        header = raw_bytes[offset:nl].decode("ascii")
        parts = header.split()
        if len(parts) != 3 or parts[0] != expected or parts[1] != "blob":
            raise ValueError(f"Unexpected git cat-file header: {header!r}")
        size = int(parts[2])
        start = nl + 1
        end = start + size
        if end + 1 > total or raw_bytes[end:end + 1] != b"\n":
            raise ValueError(f"Truncated git cat-file payload for {expected}")
        blobs[expected] = raw_bytes[start:end]
        offset = end + 1
    if offset != total:
        raise ValueError("Unexpected trailing bytes in git cat-file output")
    return blobs


def run_git_fetch(repo_dir, remote, objects, extra_args=(), attempts=3, delay=2.0):
    if not remote.startswith("https://"):
        raise ValueError(f"Refusing non-HTTPS git remote: {remote}")
    last_err = None
    for attempt in range(1, attempts + 1):
        proc = subprocess.run(
            ["git", "-C", str(repo_dir), "fetch", "--quiet", "--no-write-fetch-head", *extra_args, remote, *objects],
            capture_output=True,
            text=True,
            timeout=180,
        )
        if proc.returncode == 0:
            return
        last_err = RuntimeError(f"git fetch from {remote} failed (rc={proc.returncode}): {proc.stderr.strip()}")
        if attempt < attempts:
            time.sleep(delay * attempt)
    raise last_err


def prefetch_aports_dirs(entries):
    commits = sorted({entry["commit"] for entry in entries})
    last_err = None
    for remote in APORTS_GIT_REMOTES:
        try:
            with tempfile.TemporaryDirectory(prefix="aports-git-") as tmp:
                repo_dir = Path(tmp) / "repo"
                subprocess.run(["git", "init", "-q", str(repo_dir)], check=True, timeout=30)
                run_git_fetch(repo_dir, remote, commits, extra_args=("--depth=1", "--filter=blob:none"))

                pkg_trees = {}
                needed_blobs = set()
                for entry in entries:
                    origin, commit = entry["origin"], entry["commit"]
                    for section in ("main", "community"):
                        out = subprocess.check_output(
                            ["git", "-C", str(repo_dir), "ls-tree", commit, f"{section}/{origin}/"],
                            text=True,
                            timeout=30,
                        )
                        if not out.strip():
                            continue
                        file_shas = []
                        for line in out.splitlines():
                            meta, rel_path = line.split("\t", 1)
                            _mode, obj_type, sha = meta.split()
                            if obj_type == "blob":
                                fname = safe_name(Path(rel_path).name)
                                file_shas.append((fname, sha))
                                needed_blobs.add(sha)
                        if any(fname == "APKBUILD" for fname, _ in file_shas):
                            pkg_trees[origin] = (section, f"{remote}@{commit}:{section}/{origin}", file_shas)
                            break
                    if origin not in pkg_trees:
                        raise ValueError(f"Could not locate {origin} at commit {commit} in {remote}")

                ordered_shas = sorted(needed_blobs)
                # Batch-fetch all required APKBUILD and patch blobs in chunks so command lines stay compact.
                for idx in range(0, len(ordered_shas), 200):
                    run_git_fetch(repo_dir, remote, ordered_shas[idx:idx + 200])

                cat_proc = subprocess.run(
                    ["git", "-C", str(repo_dir), "cat-file", "--batch"],
                    input=("".join(f"{sha}\n" for sha in ordered_shas)).encode("ascii"),
                    capture_output=True,
                    check=True,
                    timeout=60,
                )
                blob_bytes = parse_cat_file_batch(cat_proc.stdout, ordered_shas)

                resolved = {}
                for origin, (section, source_ref, file_shas) in pkg_trees.items():
                    resolved[origin] = (
                        section,
                        source_ref,
                        {fname: blob_bytes[sha] for fname, sha in file_shas},
                    )
                return resolved
        except Exception as err:  # noqa: BLE001 - fall back to secondary official aports Git mirror
            last_err = err
    raise RuntimeError(f"Unable to fetch Alpine aports commits from any configured Git mirror: {last_err}") from last_err


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


def bundle_origin(entry, aports_data, work_root):
    origin = entry["origin"]
    pkg_dir = work_root / f"{origin}-{entry['version']}"
    pkg_dir.mkdir(parents=True, exist_ok=True)
    repo, aports_url, local_files = aports_data
    for fname, payload in sorted(local_files.items()):
        (pkg_dir / safe_name(fname)).write_bytes(payload)

    apkbuild_text = local_files["APKBUILD"].decode("utf-8", "replace")
    checksums = parse_apkbuild_sha512sums(apkbuild_text)

    verified_files = []
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
        "aports_source": aports_url,
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
    aports_map = prefetch_aports_dirs(entries)
    output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="alpine-sources-") as tmp:
        work_root = Path(tmp) / "tree"
        work_root.mkdir()
        with ThreadPoolExecutor(max_workers=workers) as pool:
            packages = list(pool.map(lambda item: bundle_origin(item, aports_map[item["origin"]], work_root), entries))
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
