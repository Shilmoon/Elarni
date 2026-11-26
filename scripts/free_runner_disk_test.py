"""Free a self-hosted runner's disk in a scratch tree, with the free space dictated by the test."""
import contextlib
import importlib.machinery
import importlib.util
import io
import os
from pathlib import Path
import tempfile
import time
import unittest

SCRIPT = Path(__file__).with_name('free-runner-disk').resolve()
HOUR = 3600
DAY = 24 * HOUR


def load():
    loader = importlib.machinery.SourceFileLoader('free_runner_disk', str(SCRIPT))
    module = importlib.util.module_from_spec(importlib.util.spec_from_loader('free_runner_disk', loader))
    loader.exec_module(module)
    return module


class FreeRunnerDisk(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-disk-')
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name).resolve()
        self.root, self.temp, self.go = self.base / 'checkout', self.base / 'temp', self.base / 'go-build'
        self.old_temp = self.file(self.temp / 'run-1/left.bin', age=7 * HOUR)
        self.new_temp = self.file(self.temp / 'run-2/in-use.bin', age=HOUR)
        self.old_go = self.file(self.go / 'ab/abcdef-a', age=3 * DAY)
        self.new_go = self.file(self.go / 'cd/cdef01-d', age=DAY)
        self.go_marker = self.file(self.go / 'trim.txt', age=9 * DAY)
        # Cargo marks a target directory with .rustc_info.json; the Windows check keeps a second one under .build.
        self.targets = [self.root / 'target', self.root / '.build/windows-check/bundle with spaces/rust']
        for target in self.targets:
            self.file(target / '.rustc_info.json')
            self.file(target / 'debug/incremental/elarni-1x/s-abc/query-cache.bin')
            self.file(target / 'debug/deps/libelarni.rlib')
        self.source = self.file(self.root / 'native/elarni-domain/src/lib.rs', age=30 * DAY)
        self.module = load()
        self.free = []
        self.module.free_gb = lambda path: self.free.pop(0) if len(self.free) > 1 else self.free[0]

    def file(self, path, age=0):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b'x' * 64)
        for aged in (path, path.parent):
            os.utime(aged, (time.time() - age,) * 2)
        return path

    def run_main(self, *argv):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = self.module.main(['--below', '8', '--root', str(self.root), '--temp', str(self.temp), '--go-cache', str(self.go), *argv])
        return code, output.getvalue()

    def incremental(self):
        return [target / 'debug/incremental' for target in self.targets]

    def test_enough_free_space_deletes_nothing(self):
        self.free = [8.0]
        code, output = self.run_main()
        self.assertEqual(code, 0)
        self.assertTrue(all(path.exists() for path in (self.old_temp, self.old_go, *self.incremental())), output)
        self.assertNotIn('::warning', output)

    def test_too_little_space_removes_old_temporary_files_first_and_stops_when_that_is_enough(self):
        self.free = [3.0, 9.0]
        (self.temp / 'run-4').mkdir()
        code, output = self.run_main()
        self.assertEqual(code, 0)
        self.assertTrue((self.temp / 'run-4').exists(), 'a folder a process has just made was removed')
        self.assertFalse(self.old_temp.exists())
        self.assertFalse(self.old_temp.parent.exists(), 'an emptied folder stays behind')
        self.assertTrue(self.new_temp.exists(), 'a file a running job may still use was removed')
        self.assertTrue(self.old_go.exists(), 'a later stage ran although the first freed enough')
        self.assertIn('temporary files', output)

    def test_the_go_cache_loses_only_entries_unused_for_two_days(self):
        self.free = [3.0, 3.5, 9.0]
        code, _ = self.run_main()
        self.assertEqual(code, 0)
        self.assertFalse(self.old_go.exists())
        self.assertTrue(self.new_go.exists())
        self.assertTrue(self.go_marker.exists(), "Go's own trim marker was removed")
        self.assertTrue(all(path.exists() for path in self.incremental()))

    def test_rust_incremental_caches_go_before_any_build_output(self):
        self.free = [3.0, 3.5, 4.0, 9.0]
        code, _ = self.run_main()
        self.assertEqual(code, 0)
        self.assertFalse(any(path.exists() for path in self.incremental()))
        self.assertTrue(all((target / 'debug/deps/libelarni.rlib').exists() for target in self.targets))

    def test_rust_build_outputs_go_last_and_sources_stay(self):
        self.free = [3.0, 3.5, 4.0, 4.5, 20.0]
        code, output = self.run_main()
        self.assertEqual(code, 0)
        self.assertFalse(any(target.exists() for target in self.targets))
        self.assertTrue(self.source.exists())
        self.assertTrue((self.root / '.build/windows-check').exists())
        self.assertNotIn('::warning', output)

    def test_a_disk_that_stays_full_is_reported_and_does_not_fail_the_job(self):
        self.free = [3.0]
        code, output = self.run_main()
        self.assertEqual(code, 0)
        self.assertIn('::warning', output)
        self.assertIn('3.0 GB', output)

    def test_a_file_that_cannot_be_removed_is_skipped(self):
        self.free = [3.0, 9.0]
        other = self.file(self.temp / 'run-3/also-old.bin', age=7 * HOUR)
        unlink = os.unlink

        def refuse(path, *args, **kwargs):
            if Path(path) == self.old_temp:
                raise PermissionError(13, 'in use', str(path))
            return unlink(path, *args, **kwargs)
        self.module.os.unlink = refuse
        self.addCleanup(setattr, self.module.os, 'unlink', unlink)
        code, _ = self.run_main()
        self.assertEqual(code, 0)
        self.assertTrue(self.old_temp.exists())
        self.assertFalse(other.exists())

    def test_a_missing_folder_is_skipped(self):
        self.free = [3.0, 3.0, 3.0, 9.0]
        code, _ = self.run_main('--temp', str(self.base / 'no-such-temp'), '--go-cache', str(self.base / 'no-such-cache'))
        self.assertEqual(code, 0)
        self.assertFalse(any(path.exists() for path in self.incremental()))


if __name__ == '__main__':
    unittest.main()
