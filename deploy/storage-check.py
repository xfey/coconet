#!/usr/bin/env python3
"""Read-only Linux storage measurements; never open databases or delete data."""

import argparse
import datetime
import http.client
import json
import os
from pathlib import Path
import stat
import socket
import sys
import time


def operator_status(path):
    connection = http.client.HTTPConnection("localhost", timeout=3)
    connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.sock.settimeout(3)
    try:
        connection.sock.connect(path)
        connection.request("GET", "/storage")
        response = connection.getresponse()
        data = response.read(65537)
        if response.status != 200 or len(data) > 65536:
            raise ValueError("operator status unavailable")
        return json.loads(data)
    finally:
        connection.close()


def age_seconds(value):
    return (datetime.datetime.now(datetime.timezone.utc)
            - datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))).total_seconds()


def runtime_issues(status, issue):
    maintenance, capacity = status["maintenance"], status["capacity"]
    if not maintenance.get("last_attempt_at") or age_seconds(maintenance["last_attempt_at"]) > 180:
        issue("critical", "relay", "maintenance_stalled")
    if maintenance["last_failed"]:
        issue("warning", "relay", "maintenance_failed")
    if maintenance["due_records"] >= 1024:
        issue("critical" if maintenance["due_records"] >= 8192 else "warning", "relay", "expiry_backlog")
    if maintenance.get("oldest_due_at") and age_seconds(maintenance["oldest_due_at"]) >= 3600:
        issue("warning", "relay", "expiry_delay")
    for name, used, limit in [
        ("transfer_reservation", maintenance["transfer_reserved_bytes"], maintenance["transfer_capacity_bytes"]),
        ("metadata_budget", capacity["metadata_bytes"], capacity["metadata_limit_bytes"]),
    ]:
        if used >= limit * 0.8:
            issue("critical" if used >= limit * 0.95 else "warning", "relay", name)
    if capacity["available_bytes"] < capacity["minimum_free_bytes"] + (64 << 20):
        issue("critical", "server", "write_reserve_reached")


def allocated_bytes(root, deadline, max_entries=100000):
    """Bound the walk; do not follow symlinks or descend into other filesystems."""
    root = Path(root)
    info = root.lstat()
    if not stat.S_ISDIR(info.st_mode):
        raise ValueError("monitored root must be a directory, not a symlink")
    device = info.st_dev
    pending = [root]
    seen = set()
    total = count = 0
    while pending:
        if time.monotonic() > deadline:
            raise TimeoutError("storage scan exceeded its time budget")
        path = pending.pop()
        try:
            info = path.lstat()
        except FileNotFoundError:
            if path == root:
                raise
            continue  # Normal temporary-file removal during a live scan.
        count += 1
        if count > max_entries:
            raise ValueError("storage scan exceeded its entry budget")
        if info.st_dev != device:
            raise ValueError("monitored tree contains another filesystem")
        key = (info.st_dev, info.st_ino)
        if key in seen:
            continue
        seen.add(key)
        total += info.st_blocks * 512
        if stat.S_ISDIR(info.st_mode):
            try:
                with os.scandir(path) as entries:
                    for entry in entries:
                        if count + len(pending) >= max_entries:
                            raise ValueError("storage scan exceeded its entry budget")
                        pending.append(Path(entry.path))
            except FileNotFoundError:
                if path == root:
                    raise
    return total


def validate(config):
    for key in ("free_bytes", "free_percent", "free_inode_percent"):
        limits = config[key]
        if not 0 < limits["critical"] < limits["warning"]:
            raise ValueError("invalid free-space thresholds")
        if key.endswith("percent") and limits["warning"] > 100:
            raise ValueError("invalid percentage")
    if not config["directories"]:
        raise ValueError("no monitored directories")
    names = set()
    for entry in config["directories"]:
        if not 0 < entry["warning_bytes"] < entry["critical_bytes"]:
            raise ValueError("invalid directory thresholds")
        if not Path(entry["path"]).is_absolute() or entry["name"] in names:
            raise ValueError("directory paths must be absolute and names unique")
        names.add(entry["name"])


def measure(config):
    validate(config)
    report = {"schema_version": 1, "observed_at": datetime.datetime.now(
        datetime.timezone.utc).isoformat(), "status": "ok", "directories": [],
        "filesystems": [], "issues": []}
    deadline = time.monotonic() + 20
    devices = set()

    def issue(severity, name, metric):
        report["issues"].append({"severity": severity, "name": name, "metric": metric})
        if severity == "critical" or report["status"] == "ok":
            report["status"] = severity

    for entry in config["directories"]:
        size = allocated_bytes(entry["path"], deadline)
        report["directories"].append({"name": entry["name"], "allocated_bytes": size})
        for level in ("critical", "warning"):
            if size >= entry[level + "_bytes"]:
                issue(level, entry["name"], "allocated_bytes")
                break
        device = os.stat(entry["path"]).st_dev
        if device in devices:
            continue
        devices.add(device)
        fs = os.statvfs(entry["path"])
        # Available to the unprivileged server, excluding filesystem reserves.
        values = {"free_bytes": fs.f_bavail * fs.f_frsize,
                  "free_percent": fs.f_bavail * 100 / fs.f_blocks,
                  "free_inode_percent": fs.f_favail * 100 / fs.f_files if fs.f_files else None}
        report["filesystems"].append({"path": entry["path"], **values})
        for metric, value in values.items():
            if value is None:
                continue  # Some filesystems have no fixed inode limit.
            for level in ("critical", "warning"):
                if value <= config[metric][level]:
                    issue(level, entry["name"], metric)
                    break
    if config.get("operator_socket"):
        report["operator"] = operator_status(config["operator_socket"])
        runtime_issues(report["operator"], issue)
    if config.get("backup_status"):
        path = Path(config["backup_status"])
        if not path.exists():
            issue("warning", "backup", "no_verified_backup")
        else:
            if path.stat().st_size > 65536:
                raise ValueError("oversized backup status")
            backup = json.loads(path.read_text())
            report["backup"] = backup
            if backup["status"] != "ok" or age_seconds(backup["observed_at"]) > 36 * 3600:
                issue("critical", "backup", "verified_backup_stale")
        attempt = path.with_name("last-attempt.json")
        if attempt.exists():
            if attempt.stat().st_size > 65536:
                raise ValueError("oversized backup attempt status")
            if json.loads(attempt.read_text())["status"] != "ok":
                issue("warning", "backup", "last_attempt_failed")
    return report


def save_report(report, state_file):
    if not state_file:
        return True
    path = Path(state_file)
    previous = {}
    try:
        previous = json.loads(path.read_text())
    except (FileNotFoundError, ValueError):
        pass
    # The last measurement replaces one small file. Log anomalies, recovery,
    # and a daily heartbeat; healthy checks do not create a history of files.
    emit = (report["status"] != "ok" or previous.get("status") != "ok"
            or previous.get("observed_at", "")[:10] != report["observed_at"][:10])
    temp = path.with_suffix(".tmp")
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as output:
        json.dump(report, output, sort_keys=True)
        output.write("\n")
    os.replace(temp, path)
    return emit


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--state-file")
    args = parser.parse_args()
    try:
        report = measure(json.loads(Path(args.config).read_text()))
        if save_report(report, args.state_file):
            print(json.dumps(report, sort_keys=True))
        return {"ok": 0, "warning": 1, "critical": 2}[report["status"]]
    except Exception as error:
        # A failed probe must not look healthy; never dump config or credentials.
        report = {"schema_version": 1, "observed_at": datetime.datetime.now(
            datetime.timezone.utc).isoformat(), "status": "critical",
            "issues": [{"metric": "probe_failed", "error_type": type(error).__name__}]}
        try:
            save_report(report, args.state_file)
        except Exception:
            pass
        print(json.dumps(report, sort_keys=True))
        return 2


if __name__ == "__main__":
    sys.exit(main())
