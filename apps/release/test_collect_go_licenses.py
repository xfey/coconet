import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("licenses", Path(__file__).with_name("collect_go_licenses.py"))
licenses = importlib.util.module_from_spec(spec)
spec.loader.exec_module(licenses)


class LicenseTests(unittest.TestCase):
    def test_binary_inventory_deduplicates_and_rejects_replacements(self):
        metadata = "binary: go1.26.9\n\tdep example.org/a v1.0.0 h1:hash\n\tdep example.org/a v1.0.0 h1:hash\n"
        self.assertEqual(licenses.binary_modules(metadata), ("go1.26.9", {("example.org/a", "v1.0.0")}))
        with self.assertRaises(ValueError):
            licenses.binary_modules(metadata + "\t=> ../local (devel)\n")

    def test_notices_preserve_nested_attribution_and_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source = root / "module"
            (source / "vendor").mkdir(parents=True)
            (source / "LICENSE").write_text("upstream terms\n")
            (source / "NOTICE").write_text("upstream attribution\n")
            (source / "vendor" / "COPYING.txt").write_text("nested terms\n")
            (source / "main.go").write_text("package fixture\n")
            records = licenses.copy_notices(source, root / "out")
            self.assertEqual({r["path"] for r in records}, {"LICENSE", "NOTICE", "vendor/COPYING.txt"})
            self.assertEqual((root / "out/NOTICE").read_bytes(), (source / "NOTICE").read_bytes())
            (source / "LICENSE.external").symlink_to(root / "out/LICENSE")
            with self.assertRaises(ValueError):
                licenses.copy_notices(source, root / "bad")


if __name__ == "__main__":
    unittest.main()
