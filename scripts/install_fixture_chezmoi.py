#!/usr/bin/env python3
"""Install the fixed upstream chezmoi release used by isolated CI tests."""
import argparse
import hashlib
import io
import os
from pathlib import Path
import platform
import re
import subprocess
import tarfile
import tempfile
import zipfile

VERSION = "2.69.4"
BASE = f"https://github.com/twpayne/chezmoi/releases/download/v{VERSION}"


def asset_name(system, machine):
    systems = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}
    arches = {"arm64": "arm64", "aarch64": "arm64", "amd64": "amd64", "x86_64": "amd64"}
    if system not in systems or machine.lower() not in arches:
        raise ValueError(f"unsupported fixture platform: {system}/{machine}")
    suffix = "zip" if system == "Windows" else "tar.gz"
    return f"chezmoi_{VERSION}_{systems[system]}_{arches[machine.lower()]}.{suffix}"


def verified_payload(name, checksums, archive):
    matches = []
    for line in checksums.splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1].lstrip("*") == name:
            matches.append(fields[0])
    if len(matches) != 1 or not re.fullmatch(r"[0-9a-f]{64}", matches[0]):
        raise ValueError("missing or ambiguous upstream checksum")
    if hashlib.sha256(archive).hexdigest() != matches[0]:
        raise ValueError("upstream fixture checksum mismatch")
    executable = "chezmoi.exe" if name.endswith(".zip") else "chezmoi"
    if name.endswith(".zip"):
        with zipfile.ZipFile(io.BytesIO(archive)) as stream:
            entries = [member for member in stream.infolist() if member.filename == executable]
            if len(entries) != 1 or entries[0].is_dir() or entries[0].external_attr >> 16 & 0o170000 == 0o120000:
                raise ValueError("fixture archive has no unique regular executable")
            return executable, stream.read(entries[0])
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:gz") as stream:
        entries = [member for member in stream.getmembers() if member.name == executable]
        if len(entries) != 1 or not entries[0].isfile():
            raise ValueError("fixture archive has no unique regular executable")
        return executable, stream.extractfile(entries[0]).read()


def download(name):
    # GitHub-hosted Linux, macOS, and Windows runners all provide curl. Keep
    # certificate checks and HTTPS-only redirects enabled on every platform.
    with tempfile.TemporaryDirectory(prefix="chezmoi-download-") as temporary:
        output = Path(temporary) / name
        subprocess.run([
            "curl", "--fail", "--silent", "--show-error", "--location",
            "--proto", "=https", "--proto-redir", "=https", "--retry", "3",
            "--max-time", "120", "--output", str(output), f"{BASE}/{name}",
        ], check=True)
        return output.read_bytes()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    name = asset_name(platform.system(), platform.machine())
    executable, payload = verified_payload(name, download(f"chezmoi_{VERSION}_checksums.txt").decode(), download(name))
    args.directory.mkdir(parents=True, exist_ok=True)
    destination = args.directory / executable
    destination.write_bytes(payload)
    destination.chmod(0o755)
    version = subprocess.check_output([str(destination), "--version"], text=True).strip()
    if f"version v{VERSION}" not in version and f"version {VERSION}" not in version:
        raise ValueError("downloaded fixture reports the wrong version")
    if os.environ.get("GITHUB_PATH"):
        with open(os.environ["GITHUB_PATH"], "a", encoding="utf-8") as output:
            output.write(str(args.directory.resolve()) + "\n")
    print(version)


if __name__ == "__main__":
    main()
