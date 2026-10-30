import json
from pathlib import Path
import subprocess
import sys
import unittest


SCRIPT = Path(__file__).with_name("run.py")


class LocationToolTest(unittest.TestCase):
    def invoke(self, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            capture_output=True, text=True, encoding="utf-8", timeout=5,
        )

    def test_returns_changsha(self):
        result = self.invoke("{}")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"location": "湖南长沙"})
        self.assertEqual(result.stderr, "")

    def test_defaults_to_empty_object(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"location": "湖南长沙"})

    def test_rejects_invalid_arguments(self):
        for raw in ("{", "[]", "null", '"text"', '{"location":"北京"}'):
            with self.subTest(raw=raw):
                result = self.invoke(raw)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")
                self.assertTrue(result.stderr.strip())

    def test_rejects_extra_cli_arguments(self):
        result = self.invoke("{}", "{}")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertTrue(result.stderr.strip())


if __name__ == "__main__":
    unittest.main()
