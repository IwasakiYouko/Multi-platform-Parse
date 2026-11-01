"""Offline smoke: run with YTDLP_PYTHON -I; never contact a media platform."""

import importlib
import importlib.metadata
import json
import os
import subprocess
import sys

import yt_dlp
from yt_dlp_ejs.yt import solver


if sys.prefix == sys.base_prefix:
    raise RuntimeError("the extractor must run in its isolated virtual environment")
packages = {
    item.metadata["Name"].lower().replace("_", "-"): item.version
    for item in importlib.metadata.distributions()
}
expected = {
    "yt-dlp": "2026.10.26.220202dev", "yt-dlp-ejs": "0.8.0",
    "brotli": "1.2.0", "certifi": "2026.10.23", "mutagen": "1.48.1",
    "pycryptodomex": "3.24.0", "requests": "2.34.2", "urllib3": "2.8.0",
    "websockets": "17.2", "charset-normalizer": "3.5.2", "idna": "3.20",
}
if packages != expected:
    raise RuntimeError(f"unexpected Python runtime inventory: {packages!r}")
for module in (
    "brotli", "certifi", "mutagen", "Cryptodome.Cipher.AES", "requests",
    "urllib3", "websockets.sync.client", "charset_normalizer", "idna",
):
    importlib.import_module(module)
if not callable(yt_dlp.YoutubeDL):
    raise RuntimeError("yt-dlp entrypoint unavailable")
deno = os.environ.get("YTDLP_JS_RUNTIME", "/usr/local/bin/deno")
environment = dict(os.environ, DENO_NO_UPDATE_CHECK="1", DENO_NO_PROMPT="1")
version = subprocess.run([deno, "--version"], env=environment, check=True,
                         capture_output=True, text=True, timeout=15).stdout
if version.splitlines()[0] != "deno 2.9.8 (stable, release, x86_64-unknown-linux-gnu)":
    raise RuntimeError(f"unexpected Deno version: {version!r}")
script = solver.lib() + "\nconst {meriyah, astring} = lib;\n" + solver.core() + """
if (typeof jsc !== 'function') throw new Error('EJS solver unavailable');
const solved = jsc({
  type: 'preprocessed',
  preprocessed_player: '_result.n = s => s.split("").reverse().join("");',
  requests: [{type: 'n', challenges: ['abc']}],
});
if (solved.responses[0].data.abc !== 'cba') throw new Error('EJS challenge failed');
const ast = meriyah.parse('const answer = 6 * 7;');
console.log(JSON.stringify({type: ast.type, code: astring.generate(ast)}));
"""
result = subprocess.run([
    deno, "run", "--no-config", "--no-remote", "--no-npm", "--no-prompt",
    "--deny-net", "--deny-import", "-",
], env=environment, input=script, check=True, capture_output=True, text=True, timeout=30)
if json.loads(result.stdout) != {"type": "Program", "code": "const answer = 6 * 7;\n"}:
    raise RuntimeError("bundled EJS parser/generator failed")
print("Offline yt-dlp/EJS/Deno runtime smoke passed")
