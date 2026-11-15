"""Build-time installer shared by Alpine images and CI; never invoked by the app."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import platform
import shutil
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import venv
import zipfile
import zlib


LOCK = Path(__file__).with_name("ytdlp-runtime.lock.json")


def detect_libc():
    return "glibc" if platform.libc_ver()[0] == "glibc" else "musl"


def select_wheels(artifacts, python_version, libc="glibc"):
    selected = [
        item for item in artifacts
        if item.get("python", python_version) == python_version
        and item.get("libc", libc) == libc
    ]
    required = {item["name"] for item in artifacts}
    if {item["name"] for item in selected} != required or len(selected) != len(required):
        raise ValueError(f"no unique locked runtime wheel set for CPython {python_version} ({libc})")
    return selected


def download(artifact, destination, attempts=4, delay=2.0):
    """Download a locked runtime artifact with size, SHA-256, and transient 5xx retry checks."""
    for attempt in range(1, attempts + 1):
        digest = hashlib.sha256()
        size = 0
        try:
            with urllib.request.urlopen(artifact["url"], timeout=60) as response, destination.open("wb") as output:
                if not response.url.startswith("https://"):
                    raise ValueError("runtime artifact redirected outside HTTPS")
                while block := response.read(1024 * 1024):
                    size += len(block)
                    if size > artifact["size"]:
                        raise ValueError("runtime artifact exceeds locked size")
                    digest.update(block)
                    output.write(block)
            if size != artifact["size"] or digest.hexdigest() != artifact["sha256"]:
                raise ValueError("runtime artifact checksum/size mismatch")
            return
        except urllib.error.HTTPError as err:
            if err.code not in {408, 429, 500, 502, 503, 504} or attempt >= attempts:
                raise
        except (urllib.error.URLError, TimeoutError, ConnectionError) as err:
            if attempt >= attempts:
                raise err
        time.sleep(delay * attempt)


def install_deno(archive, destination):
    with zipfile.ZipFile(archive) as package:
        if package.namelist() != ["deno"]:
            raise ValueError("unexpected Deno archive members")
        member = package.getinfo("deno")
        mode = member.external_attr >> 16
        if stat.S_ISLNK(mode) or member.file_size > 128 * 1024 * 1024:
            raise ValueError("invalid Deno executable")
        destination.parent.mkdir(parents=True, exist_ok=True)
        with package.open(member) as source, destination.open("wb") as output:
            shutil.copyfileobj(source, output)
    destination.chmod(0o755)


def collect_wheel_licenses(wheel, output):
    with zipfile.ZipFile(wheel) as package:
        for name in package.namelist():
            if name.endswith("/"):
                continue
            path = PurePosixPath(name)
            if path.is_absolute() or ".." in path.parts:
                raise ValueError("unsafe wheel member")
            legacy_notice = path.name.upper().startswith(("LICENSE", "COPYING", "NOTICE", "AUTHORS"))
            if ".dist-info/licenses/" in name or (
                ".dist-info/" in name and (legacy_notice or path.name == "METADATA")
            ):
                target = output / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(package.read(name))
        # EJS embeds meriyah's ISC and astring's MIT notices in its JS header.
        for name in package.namelist():
            if name.startswith("yt_dlp_ejs/") and name.endswith(".js"):
                source = package.read(name)
                if source.startswith(b"/*!") and b"*/" in source:
                    target = output / (PurePosixPath(name).name + ".LICENSE")
                    target.write_bytes(source.split(b"*/", 1)[0] + b"*/\n")


def copy_deno_notices(artifact, docs):
    source = LOCK.parent / "runtime-licenses" / artifact["filename"]
    payload = source.read_bytes()
    if len(payload) != artifact["size"] or hashlib.sha256(payload).hexdigest() != artifact["sha256"]:
        raise ValueError("bundled Deno notices checksum/size mismatch")
    (docs / artifact["filename"]).write_bytes(payload)


def extract_elf_audit_crates(binary):
    data = binary.read_bytes()
    if data[:4] != b"\x7fELF" or data[4] != 2 or data[5] != 1:
        raise ValueError("unexpected Deno ELF header")
    e_shoff = struct.unpack_from("<Q", data, 40)[0]
    e_shentsize, e_shnum, e_shstrndx = struct.unpack_from("<HHH", data, 58)
    shstr_off, shstr_size = struct.unpack_from("<QQ", data, e_shoff + e_shstrndx * e_shentsize + 24)
    shstr = data[shstr_off:shstr_off + shstr_size]
    for idx in range(e_shnum):
        base = e_shoff + idx * e_shentsize
        sh_name = struct.unpack_from("<I", data, base)[0]
        end = shstr.find(b"\x00", sh_name)
        name = shstr[sh_name:end].decode("ascii", "replace")
        if name == ".dep-v0":
            off, size = struct.unpack_from("<QQ", data, base + 24)
            audit = json.loads(zlib.decompress(data[off:off + size]))
            return {
                (pkg["name"], pkg["version"])
                for pkg in audit.get("packages", [])
                if pkg.get("kind", "runtime") != "build"
                and pkg.get("source") == "crates.io"
                and pkg.get("name") != "deno"
            }
    raise ValueError("Alpine Deno binary is missing cargo-auditable .dep-v0 metadata")


def install_musl_deno(destination, docs, lock, skip_deno=False, system_deno=None):
    found = Path(system_deno) if system_deno else None
    if found is None:
        which = shutil.which("deno")
        found = Path(which) if which else Path("/usr/bin/deno")
    if not found.is_file():
        raise ValueError("musl requires OS-packaged deno (install via 'apk add deno')")
    resolved = found.resolve()
    linked_crates = extract_elf_audit_crates(resolved)

    copy_deno_notices(lock["deno_alpine_notices"], docs)
    notices_archive = docs / lock["deno_alpine_notices"]["filename"]
    with tarfile.open(notices_archive, "r:gz") as bundle:
        bundled_crates = {
            m.name.split("/")[1]
            for m in bundle.getmembers()
            if m.isfile() and m.name.startswith("crates/")
        }
    needed_crates = {f"{n}-{v}" for n, v in linked_crates}
    missing = needed_crates - bundled_crates
    if missing:
        sample = ", ".join(sorted(missing)[:5])
        raise ValueError(f"bundled Alpine Deno notices missing linked crates: {sample}")
    download(lock["deno_alpine_license"], docs / "deno.LICENSE")

    if not skip_deno and destination.resolve() != resolved:
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists() or destination.is_symlink():
            destination.unlink()
        destination.symlink_to(resolved)
    return str(resolved)


def install(environment, deno, docs, skip_deno=False):
    lock = json.loads(LOCK.read_text())
    minimum = tuple(map(int, lock["python_minimum"].split(".")))
    if sys.version_info[:2] < minimum:
        raise ValueError("yt-dlp requires Python >= " + lock["python_minimum"])
    if sys.platform != "linux" or platform.machine() != "x86_64":
        raise ValueError("the pinned runtime supports Linux amd64 only")
    if sys.implementation.name != "cpython":
        raise ValueError("the pinned native wheels require CPython")
    libc = detect_libc()
    artifacts = select_wheels(lock["wheels"], f"{sys.version_info.major}.{sys.version_info.minor}", libc)
    # A clean venv has neither global packages nor pip, setuptools, or plugins.
    if environment.exists():
        raise ValueError("runtime environment already exists: " + str(environment))
    docs.mkdir(parents=True, exist_ok=True)
    musl_deno_path = None
    with tempfile.TemporaryDirectory(prefix="ytdlp-runtime-") as temporary:
        root = Path(temporary)
        wheels = []
        for artifact in artifacts:
            wheel = root / artifact["filename"]
            download(artifact, wheel)
            collect_wheel_licenses(wheel, docs / "python-licenses" / artifact["name"])
            wheels.append(str(wheel))
        # Ship the GPL dependency's exact corresponding source with its wheel.
        for artifact in lock["sources"]:
            destination = docs / "python-sources" / artifact["filename"]
            destination.parent.mkdir(parents=True, exist_ok=True)
            download(artifact, destination)
        if libc == "musl":
            musl_deno_path = install_musl_deno(deno, docs, lock, skip_deno=skip_deno)
        elif not skip_deno:
            archive = root / lock["deno"]["filename"]
            download(lock["deno"], archive)
            download(lock["deno_license"], docs / "deno.LICENSE")
            copy_deno_notices(lock["deno_notices"], docs)
            install_deno(archive, deno)
        venv.EnvBuilder(with_pip=False).create(environment)
        subprocess.run([
            sys.executable, "-I", "-m", "pip", "--isolated", "--python",
            str(environment / "bin/python"), "install", "--no-index", "--no-deps",
            "--no-compile", "--disable-pip-version-check", *wheels,
        ], check=True)
    shutil.copyfile(LOCK, docs / LOCK.name)
    (docs / "python-packages.tsv").write_text(
        "name\tversion\twheel_sha256\turl\n" + "".join(
            "\t".join(artifact[key] for key in ("name", "version", "sha256", "url")) + "\n"
            for artifact in artifacts
        )
    )
    provenance = {
        "python": sys.version,
        "python_executable": sys.executable,
        "libc": libc,
        "python_distribution": "OS package; see alpine-packages.tsv in the image" if libc == "musl" else "OS package",
    }
    if libc == "musl":
        provenance["deno"] = f"OS package ({musl_deno_path}); verified against {lock['deno_alpine_notices']['filename']}"
    elif skip_deno:
        provenance["deno"] = "skipped (--skip-deno)"
    else:
        provenance["deno"] = lock["deno"]
        provenance["deno_source_commit"] = "fe7ed012d92c642fd8e5607ac5a45d517538b5c5"
    (docs / "runtime-provenance.json").write_text(json.dumps(provenance, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--venv", type=Path, default=Path("/opt/yt-dlp"))
    parser.add_argument("--deno", type=Path, default=Path("/usr/local/bin/deno"))
    parser.add_argument("--docs", type=Path, default=Path("/usr/local/share/doc/multiparse/ytdlp-runtime"))
    parser.add_argument("--skip-deno", action="store_true", help="skip linking/installing Deno binary while verifying notices")
    args = parser.parse_args()
    install(args.venv.absolute(), args.deno.absolute(), args.docs.absolute(), skip_deno=args.skip_deno)
