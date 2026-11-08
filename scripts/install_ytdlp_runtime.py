"""Build-time installer shared by Debian images and CI; never invoked by the app."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import platform
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import venv
import zipfile


LOCK = Path(__file__).with_name("ytdlp-runtime.lock.json")


def select_wheels(artifacts, python_version):
    selected = [item for item in artifacts if item.get("python", python_version) == python_version]
    required = {item["name"] for item in artifacts}
    if {item["name"] for item in selected} != required or len(selected) != len(required):
        raise ValueError("no unique locked runtime wheel set for CPython " + python_version)
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


def install(environment, deno, docs):
    lock = json.loads(LOCK.read_text())
    minimum = tuple(map(int, lock["python_minimum"].split(".")))
    if sys.version_info[:2] < minimum:
        raise ValueError("yt-dlp requires Python >= " + lock["python_minimum"])
    if sys.platform != "linux" or platform.machine() != "x86_64":
        raise ValueError("the pinned runtime supports Linux amd64 only")
    if sys.implementation.name != "cpython":
        raise ValueError("the pinned native wheels require CPython")
    artifacts = select_wheels(lock["wheels"], f"{sys.version_info.major}.{sys.version_info.minor}")
    # A clean venv has neither global packages nor pip, setuptools, or plugins.
    if environment.exists():
        raise ValueError("runtime environment already exists: " + str(environment))
    docs.mkdir(parents=True, exist_ok=True)
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
        archive = root / lock["deno"]["filename"]
        download(lock["deno"], archive)
        download(lock["deno_license"], docs / "deno.LICENSE")
        copy_deno_notices(lock["deno_notices"], docs)
        venv.EnvBuilder(with_pip=False).create(environment)
        subprocess.run([
            sys.executable, "-I", "-m", "pip", "--isolated", "--python",
            str(environment / "bin/python"), "install", "--no-index", "--no-deps",
            "--no-compile", "--disable-pip-version-check", *wheels,
        ], check=True)
        install_deno(archive, deno)
    shutil.copyfile(LOCK, docs / LOCK.name)
    (docs / "python-packages.tsv").write_text(
        "name\tversion\twheel_sha256\turl\n" + "".join(
            "\t".join(artifact[key] for key in ("name", "version", "sha256", "url")) + "\n"
            for artifact in artifacts
        )
    )
    (docs / "runtime-provenance.json").write_text(json.dumps({
        "python": sys.version,
        "python_executable": sys.executable,
        "python_distribution": "OS package; see debian-packages.tsv and /usr/share/doc/python3*/copyright in the image",
        "deno": lock["deno"],
        "deno_source_commit": "fe7ed012d92c642fd8e5607ac5a45d517538b5c5",
    }, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--venv", type=Path, default=Path("/opt/yt-dlp"))
    parser.add_argument("--deno", type=Path, default=Path("/usr/local/bin/deno"))
    parser.add_argument("--docs", type=Path, default=Path("/usr/local/share/doc/multiparse/ytdlp-runtime"))
    args = parser.parse_args()
    install(args.venv.absolute(), args.deno.absolute(), args.docs.absolute())
