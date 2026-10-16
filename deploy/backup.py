#!/usr/bin/env python3
"""Create and restore-check a private online metadata backup; retain bounded sets."""

import argparse
import datetime
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import uuid


NAME = re.compile(r"auto-\d{8}T\d{6}Z-[0-9a-f]{8}\Z")


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=150)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def request(path, method, endpoint, body=None):
    client = UnixHTTP(path)
    try:
        client.request(method, endpoint, json.dumps(body) if body else None,
                       {"Content-Type": "application/json"})
        response = client.getresponse()
        data = response.read(65537)
        if response.status != 200 or len(data) > 65536:
            raise RuntimeError("operator request failed: HTTP " + str(response.status))
        return json.loads(data)
    finally:
        client.close()


def size(path):
    total = 0
    count = 0
    if path.is_symlink():
        raise RuntimeError("backup root must not be a symlink")
    for directory, dirs, files in os.walk(path, followlinks=False):
        for name in [".", *dirs, *files]:
            entry = Path(directory) / name
            info = entry.lstat()
            if entry.is_symlink():
                raise RuntimeError("backup tree contains a symlink")
            # Directories can be counted twice, conservatively and minimally.
            total += info.st_blocks * 512
            count += 1
            if count > 100000:
                raise RuntimeError("backup tree exceeded its entry budget")
    return total


def completed(root):
    result = []
    for directory in root.iterdir():
        if not NAME.fullmatch(directory.name) or directory.is_symlink() or not directory.is_dir():
            continue
        marker = directory / "verified.json"
        manifest = directory / "server.db.manifest.json"
        if not marker.is_file() or marker.is_symlink() or not manifest.is_file() or manifest.is_symlink():
            continue
        if marker.stat().st_size > 4096:
            raise RuntimeError("oversized backup verification marker")
        proof = json.loads(marker.read_text())
        if proof.get("name") != directory.name or proof.get("restore_verified") is not True:
            raise RuntimeError("invalid backup verification marker")
        if hashlib.sha256(manifest.read_bytes()).hexdigest() != proof.get("manifest_sha256"):
            raise RuntimeError("backup manifest changed after verification")
        result.append(directory)
    return sorted(result, key=lambda p: p.name)


def prune(root, keep, maximum, age_days, reserve=0):
    candidates = completed(root)
    now = datetime.datetime.now(datetime.timezone.utc)
    removed = []
    # Always preserve the newest two verified snapshots. Unknown directories
    # and incomplete runs count toward capacity but are never auto-deleted.
    while len(candidates) > 2:
        oldest = candidates[0]
        created = datetime.datetime.strptime(oldest.name[5:21], "%Y%m%dT%H%M%SZ").replace(tzinfo=datetime.timezone.utc)
        old = (now - created).total_seconds() > age_days * 86400
        if len(candidates) <= keep and not old and size(root) + reserve <= maximum:
            break
        size(oldest)  # Reject nested symlinks before deletion.
        shutil.rmtree(oldest)
        removed.append(oldest.name)
        candidates.pop(0)
    if size(root) + reserve > maximum:
        raise RuntimeError("backup budget insufficient while preserving the latest two verified snapshots")
    return removed


def write_json(path, data):
    temp = path.with_suffix(".tmp")
    with open(temp, "w") as output:
        os.chmod(temp, 0o600)
        json.dump(data, output, sort_keys=True)
        output.write("\n")
        output.flush()
        os.fsync(output.fileno())
    os.replace(temp, path)


def run(config):
    root = Path(config["root"])
    if not root.is_absolute() or config["keep_count"] < 2 or config["max_bytes"] < 128 << 20 or config["max_age_days"] < 1 or config["min_free_bytes"] < 64 << 20:
        raise RuntimeError("invalid backup policy")
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    if root.is_symlink() or root.stat().st_mode & 0o077:
        raise RuntimeError("backup root must be private")
    with open(root / ".lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        status = request(config["socket"], "GET", "/storage")
        metadata = status["capacity"]["metadata_bytes"]
        estimate = metadata * 2 + (64 << 20)
        if shutil.disk_usage(root).free < config["min_free_bytes"] + estimate * 2:
            raise RuntimeError("insufficient free space for backup and restore verification")
        removed = prune(root, config["keep_count"], config["max_bytes"], config["max_age_days"], estimate)
        name = "auto-" + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ-") + uuid.uuid4().hex[:8]
        directory = root / name
        if directory.exists():
            raise RuntimeError("backup destination exists")
        response = request(config["socket"], "POST", "/backup", {"name": name})
        if response.get("complete") is not True or response.get("name") != name:
            raise RuntimeError("backup completion was not confirmed")
        # Only after a completed server response may this process remove its
        # own snapshot on validation failure; a timed-out server may still copy.
        try:
            env = dict(os.environ)
            env.pop("COCONET_RELAY_HOME", None)
            result = subprocess.run([config["binary"], "restore", "--input", str(directory / "server.db"),
                                     "--data-dir", str(directory / "restore-check")],
                                    env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=180)
            if result.returncode:
                raise RuntimeError("backup restore verification failed")
            shutil.rmtree(directory / "restore-check")
            manifest = directory / "server.db.manifest.json"
            write_json(directory / "verified.json", {"name": name, "restore_verified": True,
                       "manifest_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest()})
        except Exception:
            shutil.rmtree(directory)
            raise
        removed += prune(root, config["keep_count"], config["max_bytes"], config["max_age_days"])
        report = {"status": "ok", "name": name, "restore_verified": True,
                  "allocated_bytes": size(root), "removed": removed,
                  "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
        write_json(root / "last-backup.json", report)
        write_json(root / "last-attempt.json", report)
        return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    config = None
    try:
        config = json.loads(Path(args.config).read_text())
        report = run(config)
    except Exception as error:
        report = {"status": "failed", "error": str(error),
                  "observed_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}
        if config:
            root = Path(config["root"])
            try:
                if root.is_dir() and not root.is_symlink() and not root.stat().st_mode & 0o077:
                    write_json(root / "last-attempt.json", report)
            except OSError:
                pass
        print(json.dumps(report))
        return 1
    print(json.dumps(report, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
