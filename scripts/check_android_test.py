"""Exercise check-android's verdict through a fake adb: which failed tests fail the run, and which only get reported."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('check-android').resolve()
TEST = 'com.elarni.AndroidUIE2E#slowButton'
# Stands in for adb on two emulators. FAKE_ADB_<serial digits> says how the instrumented test ends there:
# "pass", "flaky" (fails once, then passes), "failed" (fails every time) or "short" (fails once, passes alone,
# but its suite ran fewer tests than it has).
FAKE_ADB = r'''#!/usr/bin/env python3
import os, sys
from pathlib import Path
serial, command = sys.argv[2], sys.argv[3:]
api30 = serial == 'emulator-5554'
text = ' '.join(command)
if command[:3] == ['emu', 'avd', 'name']:
    print('Elarni_API_30' if api30 else 'Elarni_API_36_16K')
elif 'sys.boot_completed' in text:
    print('1')
elif 'ro.build.version.sdk' in text:
    print('30' if api30 else '36')
elif 'PAGESIZE' in text:
    print('4096' if api30 else '16384')
elif 'am instrument' in text:
    mode = os.environ.get('FAKE_ADB_' + serial[-4:], 'pass')
    counter = Path(os.environ['FAKE_ADB_STATE']) / serial
    runs = int(counter.read_text()) if counter.exists() else 0
    counter.write_text(str(runs + 1))
    print('INSTRUMENTATION_STATUS: class=com.elarni.AndroidUIE2E\nINSTRUMENTATION_STATUS: test=slowButton')
    if mode == 'pass' or mode in ('flaky', 'short') and runs:
        print('INSTRUMENTATION_STATUS_CODE: 0\n\nOK (1 test)\n\nINSTRUMENTATION_CODE: -1')
    else:
        ran = 0 if mode == 'short' else 1
        print(f'INSTRUMENTATION_STATUS_CODE: -2\n\nFAILURES!!!\nTests run: {ran},  Failures: 1\n\nINSTRUMENTATION_CODE: -1')
'''


@unittest.skipIf(os.name == 'nt', 'the fake adb is a POSIX executable')
class AcceptanceVerdict(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-android-')
        self.addCleanup(temporary.cleanup)
        self.sdk = Path(temporary.name).resolve()
        (self.sdk / 'platform-tools').mkdir()
        (self.sdk / 'state').mkdir()
        (self.sdk / 'platform-tools/adb').write_text(FAKE_ADB)
        (self.sdk / 'platform-tools/adb').chmod(0o755)

    def check(self, *serials, **modes):
        # ELARNI_CHECK_REPORT set: the script writes no check report of its own.
        env = dict(os.environ, ANDROID_HOME=str(self.sdk), FAKE_ADB_STATE=str(self.sdk / 'state'), ELARNI_CHECK_REPORT='test',
                   **{f'FAKE_ADB_{serial[-4:]}': mode for serial, mode in modes.items()})
        command = [sys.executable, str(SCRIPT), '--skip-build', '--case', 'ui', '--test-method', 'slowButton']
        for serial in serials:
            command += ['--serial', serial.replace('_', '-')]
        result = subprocess.run(command, env=env, capture_output=True, text=True, timeout=60)
        return result.returncode, result.stdout + result.stderr

    def test_a_passing_run_passes(self):
        code, output = self.check('emulator_5554')
        self.assertEqual(code, 0, output)
        self.assertIn('Android acceptance passed on Elarni_API_30', output)
        self.assertNotIn('FLAKY', output)

    def test_a_test_that_passes_when_rerun_alone_is_reported_and_does_not_fail_the_run(self):
        code, output = self.check('emulator_5554', emulator_5554='flaky')
        self.assertEqual(code, 0, output)
        self.assertIn(f'FLAKY {TEST}: failed, then passed when rerun alone', output)
        self.assertIn('Android acceptance passed on Elarni_API_30', output)

    def test_a_test_that_fails_again_alone_fails_the_run(self):
        code, output = self.check('emulator_5554', emulator_5554='failed')
        self.assertEqual(code, 1, output)
        self.assertIn(f'FAILED {TEST}: failed again when rerun alone', output)
        self.assertIn('Android acceptance failed on Elarni_API_30', output)

    def test_a_suite_that_ran_too_few_tests_fails_the_run_even_when_its_failed_test_passes_alone(self):
        code, output = self.check('emulator_5554', emulator_5554='short')
        self.assertEqual(code, 1, output)
        self.assertIn('INCOMPLETE', output)

    def test_several_emulators_pass_together_and_the_summary_names_the_flaky_test(self):
        code, output = self.check('emulator_5554', 'emulator_5556', emulator_5556='flaky')
        self.assertEqual(code, 0, output)
        self.assertIn('Summary: every suite passed', output)
        self.assertIn(f'Elarni_API_36_16K emulator-5556: FLAKY {TEST}: failed, then passed when rerun alone', output)

    def test_several_emulators_fail_when_one_has_a_test_that_fails_again(self):
        code, output = self.check('emulator_5554', 'emulator_5556', emulator_5554='flaky', emulator_5556='failed')
        self.assertEqual(code, 1, output)
        self.assertIn('Summary: 1 run(s) failed', output)
        self.assertIn(f'Elarni_API_36_16K emulator-5556: FAILED {TEST}', output)
        self.assertIn(f'Elarni_API_30 emulator-5554: FLAKY {TEST}', output)


if __name__ == '__main__':
    unittest.main()
