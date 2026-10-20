import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location("server_health_url", Path(__file__).with_name("server-health-url.py"))
HEALTH = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(HEALTH)


class HealthURLTest(unittest.TestCase):
    def test_preserves_saved_ports_and_handles_wildcard_binds(self):
        for listen, expected in {
            "127.0.0.1:8080": "127.0.0.1:8080",
            "127.0.0.1:7788": "127.0.0.1:7788",
            "0.0.0.0:9123": "127.0.0.1:9123",
            ":7788": "127.0.0.1:7788",
            "[::]:7788": "[::1]:7788",
            "[::1]:9011": "[::1]:9011",
            "192.0.2.2:7788": "192.0.2.2:7788",
        }.items():
            with self.subTest(listen=listen):
                self.assertEqual(HEALTH.health_url({"listen": listen}), "http://" + expected + "/healthz")

    def test_rejects_unusable_or_unsafe_targets(self):
        for listen in ["localhost", "127.0.0.1:0", "localhost:65536", "user@host:7788", "host/path:7788", "host:abc"]:
            with self.subTest(listen=listen), self.assertRaises(ValueError):
                HEALTH.health_url({"listen": listen})
