#!/usr/bin/env python3
"""Collect upstream notices for the modules recorded in compiled Go binaries."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tempfile

NOTICE = re.compile(r"^(?:LICENSE|LICENCE|NOTICE|COPYING|COPYRIGHT|PATENTS|UNLICENSE)(?:$|[._-])", re.I)


def run(*args):
    return subprocess.check_output(args, text=True)


def binary_modules(metadata):
    modules, version = set(), None
    for line in metadata.splitlines():
        parts = line.strip().split()
        if line and not line.startswith("\t") and parts:
            version = parts[-1]
        if parts and parts[0] == "=>":
            raise ValueError("Release licenses require dependencies without module replacements")
        if len(parts) >= 3 and parts[0] == "dep":
            module, revision = parts[1:3]
            if not revision.startswith("v") or any(p in {"", ".", ".."} for p in module.split("/")):
                raise ValueError("Invalid dependency identity")
            modules.add((module, revision))
    if version is None or not re.fullmatch(r"go\d+\.\d+\.\d+", version):
        raise ValueError("A released Go toolchain is required")
    return version, modules


def copy_notices(source, output):
    """Include nested upstream notices, preserving their relative paths verbatim."""
    records = []
    for path in sorted(source.rglob("*")):
        if not NOTICE.match(path.name) or not path.is_file():
            continue
        if path.is_symlink() or source.resolve() not in path.resolve().parents:
            raise ValueError("Notice must be a regular file inside its module")
        content = path.read_bytes()
        if not content.strip():
            continue
        relative = path.relative_to(source)
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(content)
        records.append({"path": relative.as_posix(), "sha256": hashlib.sha256(content).hexdigest()})
    if not records or not any(NOTICE.match(p.name) for p in source.iterdir() if p.is_file()):
        raise ValueError("Dependency is missing a root license/notice: " + source.name)
    return records


def collect(binaries, output):
    if output.exists() or output.is_symlink():
        raise ValueError("License output must not already exist")
    versions, modules = set(), set()
    for binary in binaries:
        version, dependencies = binary_modules(run("go", "version", "-m", str(binary)))
        versions.add(version)
        modules.update(dependencies)
    if len(versions) != 1:
        raise ValueError("All binaries must use the same Go toolchain")
    goroot = Path(run("go", "env", "GOROOT").strip())
    version = versions.pop()
    if (goroot / "VERSION").read_text().splitlines()[0] != version:
        raise ValueError("Active Go toolchain does not match the compiled binaries")
    output.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix=".go-notices-", dir=output.parent))
    try:
        entries = [{"module": "Go", "version": version, "directory": "go",
                    "files": copy_notices(goroot, stage / "go")}]
        for module, revision in sorted(modules):
            downloaded = json.loads(run("go", "mod", "download", "-json", module + "@" + revision))
            if downloaded.get("Error") or not downloaded.get("Dir"):
                raise ValueError("Could not obtain notices for " + module)
            directory = PurePosixPath("modules") / (module + "@" + revision)
            entries.append({"module": module, "version": revision, "directory": str(directory),
                            "files": copy_notices(Path(downloaded["Dir"]), stage / directory)})
        (stage / "index.json").write_text(json.dumps({"schema_version": 1, "components": entries}, indent=2) + "\n")
        (stage / "README.txt").write_text(
            "Go components linked into this release. Each component retains its upstream terms.\n"
            "index.json records module versions, notice paths and SHA-256 hashes.\n" +
            "\n".join(e["module"] + " " + e["version"] + " -> " + e["directory"] for e in entries) + "\n")
        os.rename(stage, output)
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("binaries", type=Path, nargs="+")
    args = parser.parse_args()
    collect(args.binaries, args.output)


if __name__ == "__main__":
    main()
