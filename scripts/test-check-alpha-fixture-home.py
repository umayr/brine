import importlib.util
import os
import stat
import tempfile
import unittest
from types import SimpleNamespace

spec = importlib.util.spec_from_file_location('fixture_home', os.path.join(os.path.dirname(__file__), 'check-alpha-fixture-home.py'))
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


class FixtureHomeMetadataTest(unittest.TestCase):
    def test_rejects_unsafe_component(self):
        safe = SimpleNamespace(st_mode=stat.S_IFDIR | 0o700, st_uid=42)
        self.assertTrue(check.safe_component(safe, 42, 'absent', 'absent'))
        for mode, owner, access, default in [
            (0o720, 42, 'absent', 'absent'), (0o700, 43, 'absent', 'absent'),
            (0o700, 42, 'present', 'absent'), (0o700, 42, 'absent', 'present'),
            (0o700, 42, 'unavailable', 'absent'),
        ]:
            st = SimpleNamespace(st_mode=stat.S_IFDIR | mode, st_uid=owner)
            self.assertFalse(check.safe_component(st, 42, access, default))

    @unittest.skipUnless(hasattr(os, "O_PATH"), "Linux O_PATH required")
    def test_no_symlink_traversal(self):
        with tempfile.TemporaryDirectory() as root:
            os.symlink(root, os.path.join(root, 'link'))
            with self.assertRaises(OSError):
                check.inspect(os.path.join(root, 'link'), True)


if __name__ == '__main__':
    unittest.main()
