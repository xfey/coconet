import importlib.util
import json
import hashlib
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("backup", Path(__file__).with_name("backup.py"))
BACKUP = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BACKUP)


class BackupRetentionTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def snapshot(self, index, verified=True):
        name = "auto-202610%02dT040000Z-12345678" % index
        path = self.root / name
        path.mkdir()
        manifest = path / "server.db.manifest.json"
        manifest.write_text('{"fixture":true}')
        (path / "server.db").write_bytes(b"x" * 32768)
        if verified:
            (path / "verified.json").write_text(json.dumps({"name": name, "restore_verified": True,
                       "manifest_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest()}))
        return path

    def test_retains_two_verified_snapshots_and_preserves_unknown_and_incomplete(self):
        snapshots = [self.snapshot(i) for i in range(1, 6)]
        incomplete = self.snapshot(6, False)
        unrelated = self.root / "manual-recovery"
        unrelated.mkdir()
        removed = BACKUP.prune(self.root, 2, 1 << 30, 365)
        self.assertEqual(len(removed), 3)
        self.assertTrue(all(p.exists() for p in snapshots[-2:] + [incomplete, unrelated]))

    def test_budget_refuses_to_delete_last_two(self):
        first, second = self.snapshot(1), self.snapshot(2)
        with self.assertRaises(RuntimeError):
            BACKUP.prune(self.root, 7, 100, 365)
        self.assertTrue(first.exists() and second.exists())

    def test_replaced_manifest_or_symlink_never_deleted(self):
        first = self.snapshot(1)
        self.snapshot(2)
        self.snapshot(3)
        (first / "server.db.manifest.json").write_text("changed")
        with self.assertRaises(RuntimeError):
            BACKUP.prune(self.root, 2, 1 << 30, 365)
        self.assertTrue(first.exists())
        (first / "escape").symlink_to(self.root.parent)
        with self.assertRaises(RuntimeError):
            BACKUP.size(self.root)


if __name__ == "__main__":
    unittest.main()
