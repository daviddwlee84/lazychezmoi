#!/usr/bin/env python3
"""Install pinned rg/delta test binaries into a CI-only build directory."""
import hashlib
import io
import os
from pathlib import Path
import platform
import tarfile
import urllib.request
import zipfile


ARTIFACTS = {
    "Linux": [
        ("delta", "dandavison/delta", "0.19.2", "delta-0.19.2-x86_64-unknown-linux-musl.tar.gz", "f1ea01ca7728ce3462debc359f39dfc7cbbc1a63224b71fefabf92042864aa1b"),
        ("rg", "BurntSushi/ripgrep", "15.1.0", "ripgrep-15.1.0-x86_64-unknown-linux-musl.tar.gz", "1c9297be4a084eea7ecaedf93eb03d058d6faae29bbc57ecdaf5063921491599"),
    ],
    "Windows": [
        ("delta.exe", "dandavison/delta", "0.19.2", "delta-0.19.2-x86_64-pc-windows-msvc.zip", "ac8ebb4a9f1cbee8b9ea897ba119808a244181adae6f3bed1f3b6b923c50b557"),
        ("rg.exe", "BurntSushi/ripgrep", "15.1.0", "ripgrep-15.1.0-x86_64-pc-windows-msvc.zip", "124510b94b6baa3380d051fdf4650eaa80a302c876d611e9dba0b2e18d87493a"),
    ],
}


def main():
    if platform.machine().lower() not in ("x86_64", "amd64"):
        raise SystemExit("This helper pins x86_64 Linux/Windows CI artifacts only")
    destination = Path("build/test-tools").resolve()
    destination.mkdir(parents=True, exist_ok=True)
    for name, repo, version, asset, digest in ARTIFACTS[platform.system()]:
        url = f"https://github.com/{repo}/releases/download/{version}/{asset}"
        with urllib.request.urlopen(url, timeout=60) as response:
            payload = response.read()
        if hashlib.sha256(payload).hexdigest() != digest:
            raise SystemExit(f"Checksum mismatch: {asset}")
        if asset.endswith(".zip"):
            with zipfile.ZipFile(io.BytesIO(payload)) as archive:
                matches = [p for p in archive.namelist() if p.rsplit("/", 1)[-1] == name]
                if len(matches) != 1:
                    raise SystemExit(f"Expected one {name} in {asset}")
                binary = archive.read(matches[0])
        else:
            with tarfile.open(fileobj=io.BytesIO(payload), mode="r:gz") as archive:
                matches = [p for p in archive.getmembers() if p.isfile() and p.name.rsplit("/", 1)[-1] == name]
                if len(matches) != 1:
                    raise SystemExit(f"Expected one {name} in {asset}")
                binary = archive.extractfile(matches[0]).read()
        target = destination / name
        target.write_bytes(binary)
        target.chmod(0o755)
        print(f"Installed {name} {version} for tests")
    with open(os.environ["GITHUB_PATH"], "a", encoding="utf-8") as path_file:
        path_file.write(str(destination) + "\n")


if __name__ == "__main__":
    main()
