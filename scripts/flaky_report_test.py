"""Rank tests that failed and then passed, from check results written to a scratch directory."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('flaky-report').resolve()
GO = ['sh', '/src/scripts/check-tests', 'go']
ANDROID = ['/usr/local/bin/python', 'scripts/check-android', '--avd', 'Elarni_API_30']


class FlakyReport(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-flaky-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.count = 0

    def result(self, command, code, log, revision='a' * 40, system='Linux-5.15', status='completed', day=10):
        self.count += 1
        directory = self.root / f'202611{day:02d}T0000{self.count:02d}Z-{self.count:08d}'
        directory.mkdir()
        (directory / 'summary.json').write_text(json.dumps({
            'schemaVersion': 1, 'status': status, 'exitCode': code, 'command': command, 'platform': system,
            'revision': revision, 'startedAt': f'2026-11-{day:02d}T00:00:{self.count:02d}+00:00'}))
        if log is not None:
            (directory / 'output.log').write_text(log)
        return directory

    def report(self):
        result = subprocess.run([sys.executable, str(SCRIPT), '--json', str(self.root)],
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)

    def flaky(self):
        return {(row['check'], row['test']): row['count'] for row in self.report()['flaky']}

    def test_a_flaky_line_counts_once_per_result_with_or_without_the_emulator_prefix(self):
        line = 'FLAKY com.elarni.AndroidVpnE2E#joins: failed, then passed when rerun alone\n'
        self.result(ANDROID, 1, line + '  Elarni_API_30 emulator-5556: ' + line)
        self.result(ANDROID, 1, line, revision='b' * 40)
        self.assertEqual(self.flaky(), {('check-android [Linux]', 'com.elarni.AndroidVpnE2E#joins'): 2})

    def test_the_rerun_line_of_check_tests_is_a_flaky_test(self):
        self.result(GO, 0, 'ok\nFLAKY splithttp: failed, then passed when rerun\n')
        self.assertEqual(self.flaky(), {('check-tests go [Linux]', 'splithttp'): 1})

    def test_a_go_subtest_is_counted_under_its_test(self):
        log = ('--- FAIL: TestPathChange (5.55s)\n'
               '    --- FAIL: TestPathChange/h3 (2.01s)\n'
               'FAIL\tgithub.com/xtls/xray-core/transport/internet/splithttp\t50.253s\n')
        self.result(GO, 1, log)
        self.result(GO, 0, 'ok\n')
        self.assertEqual(self.flaky(), {('check-tests go [Linux]', 'TestPathChange'): 1})

    def test_a_flutter_failure_is_named_without_its_progress_prefix(self):
        windows = ['C:\\Program Files\\Python313\\python.exe', 'scripts/check-windows']
        log = ('00:26 +10 -1: a VPN tunnel that ends by itself keeps its helper [E]\n'
               '+4 -1: 26 parallel HTTP probes complete in one round [E]\n'
               '01:04 +31 -1: Some tests failed.\n')
        self.result(windows, 1, log, system='Windows-2022Server')
        self.result(windows, 0, 'ok\n', system='Windows-2022Server')
        self.assertEqual(self.flaky(), {
            ('check-windows [Windows]', 'a VPN tunnel that ends by itself keeps its helper'): 1,
            ('check-windows [Windows]', '26 parallel HTTP probes complete in one round'): 1})

    def test_a_failure_on_a_revision_that_never_passed_is_not_flaky(self):
        self.result(GO, 1, '--- FAIL: TestBroken (0.01s)\n', revision='c' * 40)
        self.result(GO, 0, 'ok\n', revision='d' * 40)
        report = self.report()
        self.assertEqual(report['flaky'], [])
        self.assertEqual([(row['check'], row['test'], row['count']) for row in report['unresolved']],
                         [('check-tests go [Linux]', 'TestBroken', 1)])

    def test_a_pass_on_another_system_does_not_make_a_failure_flaky(self):
        self.result(GO, 1, '--- FAIL: TestOnWindows (0.01s)\n', system='Windows-2022Server')
        self.result(GO, 0, 'ok\n', system='Linux-5.15')
        report = self.report()
        self.assertEqual(report['flaky'], [])
        self.assertEqual(report['unresolved'][0]['check'], 'check-tests go [Windows]')

    def test_a_failed_check_without_a_test_name_is_reported_under_the_check(self):
        lint = ['sh', 'scripts/check-tests', 'lint']
        self.result(lint, 1, 'Diff in native/elarni-domain/src/lib.rs\n')
        self.result(lint, 0, 'ok\n')
        self.assertEqual(self.flaky(), {('check-tests lint [Linux]', '(no test name in the log)'): 1})

    def test_unfinished_and_incomplete_results_are_skipped(self):
        self.result(GO, None, '--- FAIL: TestRunning (0.01s)\n', status='running')
        self.result(GO, 130, '--- FAIL: TestInterrupted (0.01s)\n', status='interrupted')
        self.result(GO, 1, None)
        (self.root / 'broken').mkdir()
        (self.root / 'broken/summary.json').write_text('{')
        report = self.report()
        self.assertEqual(report['flaky'], [])
        self.assertEqual([row['test'] for row in report['unresolved']], ['(no test name in the log)'])
        self.assertEqual(report['results'], 1)

    def test_the_most_frequent_test_comes_first_and_shows_when_it_was_last_seen(self):
        self.result(GO, 0, 'FLAKY rare: failed, then passed when rerun\n', day=12)
        for day in (10, 11):
            self.result(GO, 0, 'FLAKY often: failed, then passed when rerun\n', day=day)
        rows = self.report()['flaky']
        self.assertEqual([(row['test'], row['count'], row['last'][:10]) for row in rows],
                         [('often', 2, '2026-11-11'), ('rare', 1, '2026-11-12')])

    def test_the_text_report_prints_one_line_per_test(self):
        self.result(GO, 0, 'FLAKY splithttp: failed, then passed when rerun\n')
        result = subprocess.run([sys.executable, str(SCRIPT), str(self.root)], capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('1  2026-11-10  check-tests go [Linux]  splithttp', result.stdout)


if __name__ == '__main__':
    unittest.main()
