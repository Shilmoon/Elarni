"""Exercise change-based check selection through git in a scratch repository."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name('check-changed').resolve()
DESKTOP = sys.platform != 'darwin'
MAC = sys.platform == 'darwin'
# CI's static checks run on Linux, so a Go or Rust change selects them there.
LINT = ['lint'] if sys.platform.startswith('linux') else []


class CheckChangedSelection(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-changed-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        (self.root / 'scripts').mkdir()
        shutil.copy(SCRIPT, self.root / 'scripts/check-changed')
        for path in ('lib/app.dart', 'native/elarni-domain/src/lib.rs', 'README.md'):
            self.write(path)
        self.git('init', '-q')
        self.git('add', '-A')
        self.git('commit', '-qm', 'fixture')

    def write(self, path, text='x\n'):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)

    def git(self, *args):
        subprocess.run(['git', '-c', 'user.name=t', '-c', 'user.email=t@t', '-c', 'core.autocrlf=false', *args],
                       cwd=self.root, check=True, capture_output=True)

    def selection(self, *args):
        result = subprocess.run([sys.executable, str(self.root / 'scripts/check-changed'), '--dry-run', *args],
                                cwd=self.root, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout.split(), result.stderr

    def test_dart_library_edit_runs_flutter_and_the_desktop_flow(self):
        self.write('lib/app.dart', 'changed\n')
        self.assertEqual(self.selection()[0], ['flutter', 'windows'] if DESKTOP else ['flutter'])

    def test_untracked_and_deleted_files_select_their_checks(self):
        self.write('Vendor/Xray-core/proxy/new.go')
        (self.root / 'native/elarni-domain/src/lib.rs').unlink()
        self.assertEqual(self.selection()[0], sorted(['go', 'rust', 'windows'] + LINT) if DESKTOP else ['go', 'rust', 'swift'])

    def test_the_wireguard_go_fork_runs_the_go_checks(self):
        self.write('Vendor/wireguard-go/device/send.go')
        self.assertEqual(self.selection()[0], sorted(['go', 'windows'] + LINT) if DESKTOP else ['go', 'swift'])

    def test_the_quic_go_forks_run_the_go_checks(self):
        for fork in ('quic-go', 'sagernet-quic-go'):
            self.write(f'Vendor/{fork}/flow_controller_connection.go')
            self.assertEqual(self.selection()[0], sorted(['go', 'windows'] + LINT) if DESKTOP else ['go', 'swift'])

    def test_documentation_only_runs_nothing(self):
        self.write('README.md', 'changed\n')
        self.write('docs/guide.md')
        self.assertEqual(self.selection(), ([], ''))

    def test_base_covers_committed_work_and_paths_with_spaces(self):
        self.write('test/a b_test.dart')
        self.git('add', '-A')
        self.git('commit', '-qm', 'work')
        self.assertEqual(self.selection()[0], [])
        self.assertEqual(self.selection('--base', 'HEAD~1')[0], ['flutter'])

    def test_images_the_desktop_flow_picks_run_it(self):
        self.write('test/windows_routing_qr.png')
        self.assertEqual(self.selection()[0], ['flutter', 'windows'] if DESKTOP else ['flutter'])

    def test_scripts_and_tun2proxy_map_to_their_own_checks(self):
        self.write('scripts/release_support.py')
        self.write('Vendor/tun2proxy/src/lib.rs')
        self.assertEqual(self.selection()[0], sorted(['rust', 'scripts'] + LINT) if DESKTOP else ['rust', 'scripts', 'swift'])

    def test_the_shadowquic_client_runs_the_go_checks_that_start_it(self):
        for path in ('Vendor/shadowquic/shadowquic/src/lib.rs', 'native/elarni-shadowquic/src/lib.rs'):
            self.write(path)
            self.assertEqual(self.selection()[0], sorted(['go', 'rust', 'windows'] + LINT) if DESKTOP else ['go', 'rust', 'swift'], path)
            (self.root / path).unlink()

    def test_an_iphone_source_builds_the_iphone_app_and_not_the_mac_suites(self):
        # The macOS suites compile nothing under ios/, so they cannot stand for a check of it.
        self.write('ios/Elarni/Elarni/IOSAppModel.swift')
        selected, notes = self.selection()
        self.assertEqual(selected, ['ios'] if MAC else [])
        self.assertEqual('ios/Elarni/Elarni/IOSAppModel.swift' in notes, not MAC)

    def test_a_mac_source_runs_the_mac_suites_and_builds_the_iphone_app_that_links_it(self):
        self.write('macos/Runner/SubscriptionStore.swift')
        selected, notes = self.selection()
        self.assertEqual(selected, ['ios', 'swift'] if MAC else [])
        self.assertEqual('macos/Runner/SubscriptionStore.swift' in notes, not MAC)

    def test_failure_output_prints_on_a_legacy_console_code_page(self):
        # Flutter frames failures with box-drawing characters, which cp1251 cannot encode.
        (self.root / 'scripts/check-tests').write_bytes(b"#!/bin/sh\nprintf '\\342\\225\\220 failure\\n'\nexit 1\n")
        (self.root / 'scripts/check-windows').write_bytes(b'raise SystemExit(0)\n')
        self.git('add', '-A')
        self.git('commit', '-qm', 'fake checks')
        self.write('lib/app.dart', 'changed\n')
        result = subprocess.run([sys.executable, str(self.root / 'scripts/check-changed')], cwd=self.root,
                                capture_output=True, timeout=60, env=dict(os.environ, PYTHONIOENCODING='cp1251'))
        self.assertNotIn(b'Traceback', result.stderr)
        self.assertIn(b'failure', result.stdout)
        self.assertIn(b'FAILED: flutter', result.stdout)
        self.assertEqual(result.returncode, 1)

    def test_all_selects_every_ci_check_for_this_os_without_changes(self):
        expected = {'darwin': ['flutter', 'go', 'ios', 'rust', 'scripts', 'swift'],
                    'linux': ['flutter', 'go', 'lint', 'race', 'rust', 'scripts', 'windows']}
        self.assertEqual(self.selection('--all')[0], expected.get(sys.platform, ['flutter', 'go', 'rust', 'scripts', 'windows']))

    def test_unmapped_platform_code_is_reported_not_ignored(self):
        self.write('android/app/Main.kt')
        self.write('.github/workflows/checks.yml')
        selected, notes = self.selection()
        self.assertEqual(selected, [])
        self.assertIn('android/app/Main.kt', notes)
        self.assertIn('.github/workflows/checks.yml', notes)
        # Nothing ran, so a real run must not report a pass.
        result = subprocess.run([sys.executable, str(self.root / 'scripts/check-changed')], cwd=self.root,
                                capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('UNCHECKED', result.stdout)

    def test_a_check_that_passed_on_its_retry_prints_its_flaky_line(self):
        (self.root / 'scripts/check-tests').write_text("#!/bin/sh\necho 'FLAKY splithttp: failed, then passed when rerun'\n")
        (self.root / 'scripts/check-windows').write_text('raise SystemExit(0)\n')
        self.git('add', '-A')
        self.git('commit', '-qm', 'fake checks')
        self.write('lib/app.dart', 'changed\n')
        result = subprocess.run([sys.executable, str(self.root / 'scripts/check-changed')], cwd=self.root,
                                capture_output=True, text=True, timeout=60)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('FLAKY splithttp', result.stdout)


class CIJobSelection(unittest.TestCase):
    """--ci-jobs: the jobs of the checks workflow that a pull request's changed paths need."""
    ALL = 'linux windows macos macos-release apple'

    def jobs(self, *paths, everything=False):
        result = subprocess.run([sys.executable, str(SCRIPT), '--ci-jobs', *(['--all'] if everything else [])],
                                input=''.join(path + '\n' for path in paths), capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result.stdout.strip()

    def test_code_shared_by_the_platforms_runs_every_job(self):
        for path in ('lib/app.dart', 'test/app_test.dart', 'Vendor/Xray-core/proxy/tuic/client.go', 'native/elarni-domain/src/lib.rs',
                     'scripts/check-tests', '.github/workflows/checks.yml', 'pubspec.lock', 'include/elarni_ffi.h', 'mise.toml'):
            self.assertEqual(self.jobs(path), self.ALL, path)

    def test_an_android_change_runs_only_the_linux_job_which_builds_android_and_reads_its_translations(self):
        self.assertEqual(self.jobs('android/app/src/main/java/com/elarni/Platform.kt', 'android/app/src/main/res/values-ru/strings.xml'), 'linux')

    def test_an_iphone_change_runs_the_iphone_build_and_the_mac_checks_that_read_its_translations(self):
        self.assertEqual(self.jobs('ios/Elarni/Elarni/Localizable.xcstrings'), 'macos apple')

    def test_a_mac_change_runs_the_three_mac_jobs(self):
        self.assertEqual(self.jobs('macos/Runner/AppDelegate.swift'), 'macos macos-release apple')

    def test_a_windows_runner_change_runs_windows_and_the_linux_build_that_installs_its_license_files(self):
        self.assertEqual(self.jobs('windows/runner/main.cpp'), 'linux windows')
        self.assertEqual(self.jobs('windows/GOZXING_LICENSE.txt'), 'linux windows')

    def test_a_linux_runner_change_runs_only_linux(self):
        self.assertEqual(self.jobs('linux/runner/my_application.cc'), 'linux')

    def test_platform_changes_add_up_and_one_shared_file_runs_every_job(self):
        self.assertEqual(self.jobs('android/app/build.gradle.kts', 'ios/Elarni/Elarni/Info.plist'), 'linux macos apple')
        self.assertEqual(self.jobs('android/app/build.gradle.kts', 'lib/app.dart'), self.ALL)

    def test_documentation_runs_no_job_and_does_not_widen_a_platform_change(self):
        self.assertEqual(self.jobs('README.md', 'docs/TESTING.md', 'docs/images/main.png', 'LICENSE'), '')
        self.assertEqual(self.jobs('CHANGELOG.md', 'android/README.md', 'android/app/build.gradle.kts'), 'linux')

    def test_a_file_list_that_was_cut_short_runs_every_job(self):
        # The workflow passes "many" when GitHub lists more files than it returns.
        self.assertEqual(self.jobs('many'), self.ALL)

    def test_all_runs_every_job_for_any_code_and_none_for_documentation(self):
        self.assertEqual(self.jobs('android/app/build.gradle.kts', everything=True), self.ALL)
        self.assertEqual(self.jobs('README.md', everything=True), '')


if __name__ == '__main__':
    unittest.main()
