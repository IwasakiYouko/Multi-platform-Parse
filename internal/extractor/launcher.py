# yt-dlp integration semantics adapted from ParseHub 2.3.15.
# Copyright (c) 2024 梓澪. MIT license: see LICENSE.parsehub.
"""Private stdin/stdout protocol. No user-provided command or yt-dlp options."""
import contextlib
# ctypes initializes its interpreter handle once; later dlopen is crypto-only.
import ctypes
import io
import json
import os
import pathlib
import resource
import shutil
import socket
import stat
import subprocess
import sys
import urllib.parse

# The verified dev0 wheel preserves this pre-normalized version in METADATA.
VERSION = "2026.10.26.220202dev"
EJS_VERSION = "0.8.0"
MAX_JSON = 8 << 20
MAX_FILES = 64
MEDIA_EXTS = {"mp4", "mkv", "webm", "mov", "m4a", "mp3", "ogg", "opus", "flac", "wav", "aac", "ts"}
PROTOCOLS = {"http", "https", "m3u8", "m3u8_native", "http_dash_segments", "http_dash_segments_generator"}
DEMUXERS = "mov,matroska,webm,mpegts,aac,mp3,ogg,flac,wav,avi"


class PolicyError(PermissionError):
    pass


class BudgetError(Exception):
    pass


def check_budget(root, limit):
    total = count = 0
    for parent, dirs, files in os.walk(root, followlinks=False):
        for name in dirs + files:
            path = os.path.join(parent, name)
            st = os.lstat(path)
            if stat.S_ISLNK(st.st_mode):
                raise PolicyError()
            if stat.S_ISDIR(st.st_mode):
                continue
            count += 1
            if not stat.S_ISREG(st.st_mode) or count > MAX_FILES:
                raise BudgetError()
            total += st.st_size
            if total > limit:
                raise BudgetError()
    return total


class Policy:
    def __init__(self, request):
        self.root = os.path.realpath(request["dir"])
        self.limit = request["limit"]
        proxy = urllib.parse.urlsplit(request["proxy"])
        if proxy.scheme != "http" or proxy.hostname != "127.0.0.1" or not proxy.port or not proxy.username or not proxy.password:
            raise PolicyError()
        self.gateway = (proxy.hostname, proxy.port)
        self.deno = os.path.realpath(request["runtime"])
        self.ffmpeg = os.path.realpath(request["ffmpeg"])
        self.ffprobe = os.path.realpath(request["ffprobe"])
        self.crypto_roots = tuple(os.path.realpath(os.path.join(path, "Cryptodome")) + os.sep for path in sys.path if os.path.isdir(os.path.join(path, "Cryptodome")))
        self.executing = False
        self.children = []

    def local(self, value, existing=False):
        if isinstance(value, bytes):
            value = os.fsdecode(value)
        if not isinstance(value, str):
            raise PolicyError()
        if value.startswith("file:"):
            value = value[5:]
        if ":" in value or "\x00" in value:
            raise PolicyError()
        path = os.path.abspath(value)
        real = os.path.realpath(path)
        if not real.startswith(self.root + os.sep) or real != path:
            raise PolicyError()
        if existing:
            st = os.lstat(path)
            if not stat.S_ISREG(st.st_mode) or pathlib.Path(path).suffix[1:].lower() not in MEDIA_EXTS:
                raise PolicyError()
        return path

    def audit(self, event, args):
        if event == "import":
            name = args[0]
            if name.split(".")[0] in {"curl_cffi", "yt_dlp_plugins", "ytdlp_plugins"}:
                raise ImportError("native network clients and plugins are disabled")
        if event == "socket.__new__":
            _, family, kind, protocol = args
            if family != socket.AF_INET or kind & 15 != socket.SOCK_STREAM or protocol not in (0, socket.IPPROTO_TCP):
                raise PolicyError()
        elif event in ("socket.connect", "socket.getaddrinfo"):
            address = args[1] if event == "socket.connect" else args[:2]
            if not isinstance(address, tuple) or tuple(address[:2]) != self.gateway:
                raise PolicyError()
        elif event.startswith("socket.") and event in {"socket.bind", "socket.sendto", "socket.sendmsg", "socket.gethostbyname", "socket.gethostbyaddr", "socket.getnameinfo"}:
            raise PolicyError()
        elif event in {"ctypes.dlopen", "ctypes.dlsym"}:
            library = args[0] if event == "ctypes.dlopen" else getattr(args[0], "_name", None)
            if not isinstance(library, str):
                raise PolicyError()
            path = os.path.realpath(library)
            if not path.endswith(".so") or not path.startswith(self.crypto_roots):
                raise PolicyError()
        elif event in {"os.system", "os.posix_spawn", "os.posix_spawnp", "os.fork", "os.forkpty", "os.exec"}:
            raise PolicyError()
        elif event == "subprocess.Popen":
            if not self.executing:
                raise PolicyError()
            executable, argv, cwd, env = args
            self.validate_process(executable, argv)
            if cwd is not None and os.path.realpath(cwd) != self.root:
                raise PolicyError()
        elif event == "open":
            path, mode, flags = args
            if isinstance(path, int):
                return
            if flags & (os.O_WRONLY | os.O_RDWR | os.O_CREAT | os.O_TRUNC | os.O_APPEND):
                if os.fsdecode(path) == os.devnull:
                    return
                self.local(os.fsdecode(path))
        elif event in {"os.remove", "os.rmdir", "os.mkdir", "os.chmod", "os.truncate", "os.utime"}:
            path = args[0]
            if not isinstance(path, int):
                self.local(os.fsdecode(path))
            # dir_fd opens must not silently reinterpret an already checked pathname.
            if event in {"os.remove", "os.rmdir", "os.mkdir"} and args[-1] not in (None, -1):
                raise PolicyError()
        elif event in {"os.rename", "os.link"}:
            self.local(os.fsdecode(args[0]))
            self.local(os.fsdecode(args[1]))
            if any(x not in (None, -1) for x in args[2:]):
                raise PolicyError()
        elif event == "os.symlink":
            raise PolicyError()

    def validate_process(self, executable, argv):
        executable = os.path.realpath(os.fsdecode(executable))
        if not isinstance(argv, (list, tuple)) or not argv or any(not isinstance(a, str) for a in argv):
            raise PolicyError()
        if os.path.realpath(argv[0]) != executable:
            raise PolicyError()
        if executable == self.deno:
            if argv[1:] == ["--version"]:
                return
            if len(argv) < 4 or argv[1] != "run" or argv[-1] != "-":
                raise PolicyError()
            required = {"--deny-net", "--deny-import", "--deny-read", "--deny-write", "--deny-env", "--deny-run", "--deny-ffi", "--no-remote", "--no-npm", "--no-config", "--no-prompt"}
            if not required.issubset(argv):
                raise PolicyError()
            for arg in argv[2:-1]:
                if arg.startswith(("--allow", "--inspect", "--unsafely", "--location", "--import-map")) or arg in {"-A", "--allow-all"} or not arg.startswith("--"):
                    raise PolicyError()
            return
        if executable not in {self.ffmpeg, self.ffprobe}:
            raise PolicyError()
        if "-protocol_whitelist" not in argv or "-format_whitelist" not in argv:
            raise PolicyError()
        for i, arg in enumerate(argv):
            if arg == "-protocol_whitelist" and (i+1 == len(argv) or argv[i+1] != "file,pipe"):
                raise PolicyError()
            if arg == "-format_whitelist" and (i+1 == len(argv) or argv[i+1] != DEMUXERS):
                raise PolicyError()
            if "://" in arg or arg.startswith(("http:", "https:", "tcp:", "udp:", "concat:", "subfile:", "crypto:")):
                raise PolicyError()
            if arg == "-i":
                if i+1 == len(argv):
                    raise PolicyError()
                self.local(argv[i+1], existing=True)
        # Version/feature probes contain no inputs; ffprobe uses a positional input.
        probes = {"-version", "-bsfs", "-codecs", "-formats", "-filters", "-encoders", "-decoders"}
        if any(p in argv for p in probes):
            return
        if executable == self.ffprobe:
            if "-i" not in argv:
                self.local(argv[-1], existing=True)
        else:
            if "-i" not in argv:
                raise PolicyError()
            self.local(argv[-1])

    def install(self):
        original = subprocess.Popen.__init__
        policy = self

        def guarded_init(proc, args, *positional, **kwargs):
            if positional or kwargs.get("shell") or kwargs.get("executable") or kwargs.get("preexec_fn") or kwargs.get("start_new_session") or kwargs.get("process_group"):
                raise PolicyError()
            if not isinstance(args, (tuple, list)) or not args:
                raise PolicyError()
            argv = list(args)
            binary = shutil.which(argv[0]) if not os.path.isabs(argv[0]) else argv[0]
            if binary is None:
                raise PolicyError()
            binary = os.path.realpath(binary)
            argv[0] = binary
            if binary == policy.deno and argv[1:] != ["--version"]:
                # The solver is a trusted bundled stdin script, not an npm/remote module.
                if len(argv) < 3 or argv[1] != "run" or argv[-1] != "-":
                    raise PolicyError()
                argv[-1:-1] = ["--deny-net", "--deny-import", "--deny-read", "--deny-write", "--deny-env", "--deny-run", "--deny-ffi", "--no-npm", "--cached-only", "--v8-flags=--max-old-space-size=256"]
            elif binary in {policy.ffmpeg, policy.ffprobe}:
                restrictions = ["-protocol_whitelist", "file,pipe", "-format_whitelist", DEMUXERS]
                # Input options apply to every input, not merely the first stream.
                if "-i" in argv:
                    out = [argv[0]]
                    for arg in argv[1:]:
                        if arg == "-i":
                            out.extend(restrictions)
                        out.append(arg)
                    argv = out
                else:
                    argv[1:1] = restrictions
            policy.validate_process(binary, argv)
            policy.children[:] = [child for child in policy.children if child.poll() is None]
            if len(policy.children) >= 4:
                raise PolicyError()
            kwargs["cwd"] = policy.root
            kwargs["close_fds"] = True
            policy.executing = True
            try:
                original(proc, argv, **kwargs)
                policy.children.append(proc)
            finally:
                policy.executing = False

        subprocess.Popen.__init__ = guarded_init
        sys.addaudithook(self.audit)


def scrub(info, depth=0):
    if depth > 80:
        raise PolicyError()
    if isinstance(info, dict):
        clean = {}
        for key, value in info.items():
            lower = key.lower()
            if key.startswith("__") or lower in {"cookies", "cookie", "cookiefile", "cookiesfrombrowser", "proxy", "proxies", "authorization", "proxy-authorization", "x-youtube-identity-token", "filepath", "filename", "_filename", "infojson_filename", "_postprocessors", "postprocessors", "downloader_options", "external_downloader", "external_downloader_args", "section_start", "section_end", "requested_downloads", "requested_formats", "requested_subtitles", "impersonate"}:
                continue
            if lower == "http_headers" and isinstance(value, dict):
                value = {k:v for k,v in value.items() if k.lower() not in {"cookie", "authorization", "proxy-authorization", "x-youtube-identity-token"}}
            clean[key] = scrub(value, depth+1)
        return clean
    if isinstance(info, (list, tuple)):
        return [scrub(item, depth+1) for item in info]
    if isinstance(info, str):
        return info
    if info is None or isinstance(info, (int, float, bool)):
        return info
    raise PolicyError()


def validate_info(info, depth=0):
    if depth > 80 or not isinstance(info, dict):
        raise PolicyError()
    if info.get("live_status") in {"is_live", "is_upcoming", "was_live"} or info.get("is_live"):
        raise ValueError("live")
    if info.get("_type", "video") not in {"video", "playlist", "multi_video"}:
        raise PolicyError()
    for key in ("id", "ext", "format_id"):
        value = info.get(key)
        if value is not None and (not isinstance(value, str) or len(value) > 256 or any(c in value for c in "/\\\x00\r\n") or value in {".", ".."}):
            raise PolicyError()
    protocol = info.get("protocol")
    if protocol is not None and (not isinstance(protocol, str) or any(p not in PROTOCOLS | {"mhtml"} for p in protocol.split("+"))):
        raise PolicyError()
    for key in ("url", "manifest_url", "fragment_base_url"):
        value = info.get(key)
        if value:
            parsed = urllib.parse.urlsplit(value)
            if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
                raise PolicyError()
    for item in info.get("fragments") or []:
        if not isinstance(item, dict):
            raise PolicyError()
        value = item.get("url")
        if value:
            parsed = urllib.parse.urlsplit(value)
            if parsed.scheme and (parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username):
                raise PolicyError()
    for key in ("formats", "entries"):
        values = info.get(key) or []
        if not isinstance(values, list) or len(values) > 10000:
            raise PolicyError()
        for value in values:
            validate_info(value, depth+1)


def cookies(cookie):
    # Reference conversion: a host-scoped Netscape jar, never a global Cookie header.
    values = []
    for part in cookie.split(";"):
        name, sep, value = part.strip().partition("=")
        if not sep or not name:
            continue
        if any(c in name+value for c in "\t\r\n\x00"):
            raise PolicyError()
        values.append((name, value))
    text = "# Netscape HTTP Cookie File\n" + "".join(f".youtube.com\tTRUE\t/\tFALSE\t0\t{name}\t{value}\n" for name, value in values)
    return io.StringIO(text)


class QuietLogger:
    def debug(self, *_):
        pass
    warning = error = debug


def execute(request):
    if request.get("mode") not in {"resolve", "download"} or request.get("platform") not in {"youtube", "facebook", "snapchat"}:
        raise PolicyError()
    if not isinstance(request.get("limit"), int) or request["limit"] <= 0:
        raise PolicyError()
    policy = Policy(request)
    sys.dont_write_bytecode = True
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_NOFILE, (96, 96))
    resource.setrlimit(resource.RLIMIT_FSIZE, (request["limit"], request["limit"]))
    cpu = 120 if request["mode"] == "resolve" else 900
    resource.setrlimit(resource.RLIMIT_CPU, (cpu, cpu))
    # V8 reserves a large virtual cage; CPU, descriptor, file and wall-time caps
    # remain inherited by every child without a virtual-address-space cap.
    policy.install()
    import importlib.metadata
    try:
        if importlib.metadata.version("yt-dlp") != VERSION or importlib.metadata.version("yt-dlp-ejs") != EJS_VERSION:
            return {"error":"runtime_missing"}
        import yt_dlp
        from yt_dlp.networking.common import _REQUEST_HANDLERS
        from yt_dlp.globals import all_plugins_loaded, plugin_dirs
        plugin_dirs.value = []
        all_plugins_loaded.value = True
        # Optional native clients could open sockets outside Python's audit hooks.
        for name in tuple(_REQUEST_HANDLERS):
            if name != "Urllib":
                del _REQUEST_HANDLERS[name]
    except (ImportError, importlib.metadata.PackageNotFoundError):
        return {"error":"runtime_missing"}
    cookiefile = cookies(request.get("cookie", "") if request["platform"] == "youtube" else "")
    opts = {
        "quiet":True, "no_warnings":True, "noprogress":True, "logger":QuietLogger(),
        "noplaylist":True, "playlistend":1, "cachedir":False,
        "proxy":request["proxy"], "socket_timeout":15, "retries":3,
        "fragment_retries":3, "extractor_retries":2, "file_access_retries":0,
        "js_runtimes":{"deno":{"path":request["runtime"]}}, "remote_components":[],
        "plugin_dirs":[], "allowed_extractors":[{"youtube":"youtube.*", "facebook":"facebook.*", "snapchat":"snapchat.*"}[request["platform"]]],
        "cookiefile":cookiefile, "cookiesfrombrowser":None, "enable_file_urls":False,
        "external_downloader":"native", "hls_prefer_native":True,
        "concurrent_fragment_downloads":1, "http_chunk_size":1<<20,
        "max_filesize":request["limit"], "buffersize":64<<10, "noresizebuffer":True,
        "outtmpl":os.path.join(policy.root, "media.%(ext)s"),
        "paths":{"home":policy.root,"temp":policy.root}, "overwrites":False,
        "ffmpeg_location":request["ffmpeg"], "fixup":"detect_or_warn",
        "progress_hooks":[lambda _:check_budget(policy.root, request["limit"])],
        "postprocessor_hooks":[lambda _:check_budget(policy.root, request["limit"])],
        "writeinfojson":False, "writethumbnail":False, "writesubtitles":False,
        "writeautomaticsub":False, "getcomments":False,
    }
    if request["mode"] == "download":
        opts["format_sort"] = ["+codec:h264", "filesize~500M"]

    class NoReextractDL(yt_dlp.YoutubeDL):
        def download(self, *args, **kwargs):
            # load-info-json must never silently retry by extracting a webpage.
            raise PolicyError()

    with contextlib.redirect_stdout(sys.stderr):
        with NoReextractDL(opts) as ydl:
            if request["mode"] == "resolve":
                target = request["url"]
                if request["platform"] == "snapchat":
                    parsed = urllib.parse.urlsplit(target)
                    video_id = parsed.path.rstrip("/").rsplit("/", 1)[-1]
                    target = urllib.parse.urlunsplit(parsed._replace(path="/spotlight/" + video_id))
                    from yt_dlp.extractor.snapchat import SnapchatSpotlightIE
                    # Keep the reference's complete ID alphabet instead of truncating '-'.
                    SnapchatSpotlightIE._VALID_URL = r"https?://(?:www\.)?snapchat\.com/spotlight/(?P<id>[a-zA-Z0-9_-]+)"
                    ydl.add_info_extractor(SnapchatSpotlightIE())
                info = ydl.extract_info(target, download=False)
                if not isinstance(info, dict):
                    raise PolicyError()
                if info.get("live_status") in {"is_live", "is_upcoming", "was_live"}:
                    raise ValueError("live")
                if info.get("_type") == "playlist":
                    entries = info.get("entries") or []
                    if not entries:
                        raise PolicyError()
                    info = next(iter(entries))
                info = scrub(ydl.sanitize_info(info))
                validate_info(info)
                encoded = json.dumps(info, ensure_ascii=False, allow_nan=False)
                if len(encoded.encode("utf-8")) > MAX_JSON:
                    return {"error":"metadata_limit"}
                return {"info":info}
            info = scrub(request["info"])
            if "formats" in info:
                info["formats"] = [f for f in info["formats"] if f.get("protocol") != "mhtml"]
            validate_info(info)
            if info.get("_type", "video") != "video":
                raise PolicyError()
            # Filesystem directives are dropped recursively before load-info-json.
            info_path = os.path.join(policy.root, "input.info.json")
            with open(info_path, "w", encoding="utf-8") as output:
                json.dump(info, output, allow_nan=False)
            if ydl.download_with_info_file(info_path):
                raise RuntimeError()
            os.remove(info_path)
            check_budget(policy.root, request["limit"])
            files = []
            for name in sorted(os.listdir(policy.root)):
                path = os.path.join(policy.root, name)
                if os.path.isfile(path) and pathlib.Path(name).suffix[1:].lower() in MEDIA_EXTS:
                    policy.local(path, existing=True)
                    files.append(name)
            if not files:
                raise RuntimeError()
            return {"files":files}


def main():
    try:
        raw = sys.stdin.buffer.read(MAX_JSON + (1 << 20) + 1)
        if len(raw) > MAX_JSON + (1 << 20):
            result = {"error":"metadata_limit"}
        else:
            result = execute(json.loads(raw))
    except BudgetError:
        result = {"error":"budget"}
    except PolicyError:
        result = {"error":"policy"}
    except ValueError as error:
        result = {"error":"live" if str(error) == "live" else "extraction"}
    except BaseException:
        # Never return raw extractor logs, URLs, cookies, or proxy credentials.
        result = {"error":"extraction"}
    sys.stdout.write(json.dumps(result, ensure_ascii=False, allow_nan=False))


if __name__ == "__main__":
    main()
