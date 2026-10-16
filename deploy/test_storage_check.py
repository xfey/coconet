import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("storage-check.py")
SPEC = importlib.util.spec_from_file_location("storage_check", SCRIPT)
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class StorageCheckTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.data = self.root / "data"
        self.data.mkdir()
        self.config = {
            "free_bytes": {"warning": 1000, "critical": 500},
            "free_percent": {"warning": 20, "critical": 10},
            "free_inode_percent": {"warning": 10, "critical": 5},
            "directories": [{"name": "data", "path": str(self.data),
                             "warning_bytes": 1000000, "critical_bytes": 2000000}],
        }

    def test_measures_blocks_without_following_symlinks_or_counting_hardlinks_twice(self):
        source = self.data / "source"
        source.write_bytes(b"x" * 8192)
        os.link(source, self.data / "hardlink")
        outside = self.root / "outside"
        outside.mkdir()
        (outside / "large").write_bytes(b"x" * 100000)
        link = self.data / "link"
        link.symlink_to(outside, target_is_directory=True)
        expected = sum(p.lstat().st_blocks * 512 for p in [self.data, source, link])
        self.assertEqual(CHECK.allocated_bytes(self.data, time.monotonic() + 5), expected)
        with self.assertRaises(ValueError):
            CHECK.allocated_bytes(link, time.monotonic() + 5)

    def test_missing_directory_and_bounded_scan_fail(self):
        with self.assertRaises(FileNotFoundError):
            CHECK.allocated_bytes(self.root / "missing", time.monotonic() + 5)
        with self.assertRaises(TimeoutError):
            CHECK.allocated_bytes(self.data, time.monotonic() - 1)
        for i in range(10):
            (self.data / str(i)).touch()
        with self.assertRaises(ValueError):
            CHECK.allocated_bytes(self.data, time.monotonic() + 5, max_entries=5)

    def test_thresholds_use_unprivileged_available_space_and_inodes(self):
        fs = SimpleNamespace(f_bavail=1000, f_bfree=9000, f_frsize=1,
                             f_blocks=10000, f_favail=50, f_files=1000)
        with patch.object(CHECK.os, "statvfs", return_value=fs):
            report = CHECK.measure(self.config)
        self.assertEqual(report["status"], "critical")
        self.assertEqual({(i["metric"], i["severity"]) for i in report["issues"]}, {
            ("free_bytes", "warning"), ("free_percent", "critical"),
            ("free_inode_percent", "critical")})
        self.assertEqual(report["filesystems"][0]["free_bytes"], 1000)

    def test_directory_threshold_and_shared_filesystem(self):
        config = copy.deepcopy(self.config)
        config["directories"][0].update(warning_bytes=1, critical_bytes=2)
        config["directories"].append({**config["directories"][0], "name": "second"})
        fs = SimpleNamespace(f_bavail=9000, f_frsize=1,
                             f_blocks=10000, f_favail=900, f_files=1000)
        with patch.object(CHECK, "allocated_bytes", return_value=2), patch.object(
                CHECK.os, "statvfs", return_value=fs):
            report = CHECK.measure(config)
        self.assertEqual(len(report["filesystems"]), 1)
        self.assertEqual(report["status"], "critical")
        self.assertEqual(len(report["issues"]), 2)

    def test_status_replaces_file_suppresses_healthy_noise_and_records_recovery(self):
        state = self.root / "latest.json"
        report = {"status": "ok", "observed_at": "2026-10-12T01:00:00+00:00"}
        self.assertTrue(CHECK.save_report(report, state))
        self.assertFalse(CHECK.save_report(report, state))
        warning = {**report, "status": "warning"}
        self.assertTrue(CHECK.save_report(warning, state))
        self.assertTrue(CHECK.save_report(report, state))
        self.assertTrue(CHECK.save_report({**report, "observed_at": "2026-10-13T01:00:00+00:00"}, state))
        self.assertEqual(state.stat().st_mode & 0o777, 0o600)
        self.assertFalse(state.with_suffix(".tmp").exists())

    def test_probe_failure_overwrites_old_healthy_status_and_exits_nonzero(self):
        config_path = self.root / "config.json"
        self.config["directories"][0]["path"] = str(self.root / "missing")
        config_path.write_text(json.dumps(self.config))
        state = self.root / "latest.json"
        state.write_text('{"status":"ok"}')
        result = subprocess.run([sys.executable, str(SCRIPT), "--config", str(config_path),
                                 "--state-file", str(state)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(json.loads(result.stdout)["issues"][0]["metric"], "probe_failed")
        self.assertEqual(json.loads(state.read_text())["status"], "critical")

    def test_reversed_thresholds_rejected(self):
        self.config["free_bytes"] = {"warning": 100, "critical": 200}
        with self.assertRaises(ValueError):
            CHECK.measure(self.config)

    def test_runtime_reports_backlog_and_failed_maintenance(self):
        now = CHECK.datetime.datetime.now(CHECK.datetime.timezone.utc).isoformat()
        status = {"maintenance": {"last_attempt_at": now, "last_failed": True,
                  "due_records": 10000, "transfer_reserved_bytes": 96, "transfer_capacity_bytes": 100},
                  "capacity": {"metadata_bytes": 1, "metadata_limit_bytes": 100,
                               "available_bytes": 1 << 30, "minimum_free_bytes": 1 << 20}}
        issues = []
        CHECK.runtime_issues(status, lambda *args: issues.append(args))
        self.assertIn(("warning", "relay", "maintenance_failed"), issues)
        self.assertIn(("critical", "relay", "expiry_backlog"), issues)
        self.assertIn(("critical", "relay", "transfer_reservation"), issues)


if __name__ == "__main__":
    unittest.main()
