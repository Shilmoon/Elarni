"""Wait for a workflow run through a stand-in for the gh command."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
from pathlib import Path
import unittest

SCRIPT = Path(__file__).with_name('ci-wait').resolve()


def load():
    loader = importlib.machinery.SourceFileLoader('ci_wait', str(SCRIPT))
    module = importlib.util.module_from_spec(importlib.util.spec_from_loader('ci_wait', loader))
    loader.exec_module(module)
    return module


class CiWait(unittest.TestCase):
    def setUp(self):
        self.module = load()
        self.calls = []
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'success'}]
        self.conclusion = 'success'
        self.runs = [{'databaseId': 77, 'event': 'pull_request', 'status': 'in_progress', 'headSha': 'a' * 40}]
        self.log = ''
        # What the repository's API answers: its self-hosted runners, and the run's jobs with their runs-on labels.
        self.runners = [{'name': 'linux-server', 'status': 'online', 'labels': [{'name': 'self-hosted'}, {'name': 'elarni-linux'}]},
                        {'name': 'windows-vm', 'status': 'online', 'labels': [{'name': 'self-hosted'}, {'name': 'elarni-windows'}]}]
        self.queued = []
        self.runners_refused = False
        # One entry per "gh run watch" that ends by its time limit, the run still going; called before the next one.
        self.watch_timeouts = []
        self.module.gh = self.gh

    def gh(self, *args, check=True, timeout=None):
        self.calls.append(args)
        if args[:2] == ('run', 'list'):
            return 0, json.dumps(self.runs)
        if args[:2] == ('run', 'watch'):
            if self.watch_timeouts:
                self.assertIsNotNone(timeout)
                self.watch_timeouts.pop(0)()
                return None, ''
            return (0 if self.conclusion == 'success' else 1), ''
        if args[0] == 'api' and args[1].endswith('/actions/runners'):
            return (1, '') if self.runners_refused else (0, json.dumps({'runners': self.runners}))
        if args[0] == 'api' and '/actions/runs/77/jobs' in args[1]:
            return 0, json.dumps({'jobs': self.queued})
        if args[:2] == ('run', 'view') and '--log-failed' in args:
            return 0, self.log
        if args[:2] == ('run', 'view'):
            return 0, json.dumps({'conclusion': self.conclusion, 'attempt': 1, 'url': 'https://example.invalid/run/77',
                                  'jobs': self.jobs})
        raise AssertionError(f'unexpected gh call: {args}')

    def run_main(self, *argv):
        output = io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            code = self.module.main(list(argv))
        return code, output.getvalue()

    def test_a_green_run_prints_one_line_per_job_and_exits_zero(self):
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertIn('success  linux checks and release', output)
        self.assertIn(('run', 'watch', '77', '--exit-status', '--interval', '30'), self.calls)
        self.assertFalse(any('--log-failed' in call for call in self.calls))

    def test_a_failed_run_prints_the_end_of_each_failed_job_and_exits_one(self):
        self.conclusion = 'failure'
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'failure'},
                     {'databaseId': 2, 'name': 'macos-15 checks', 'conclusion': 'success'}]
        self.log = ''.join(f'linux checks and release\tRun checks\t2026-11-16T00:00:00.0000000Z line {number}\n'
                           for number in range(500))
        code, output = self.run_main('77', '--lines', '20')
        self.assertEqual(code, 1)
        self.assertIn('failure  linux checks and release', output)
        self.assertIn('line 499', output)
        self.assertNotIn('line 479\n', output)
        self.assertNotIn('2026-11-16T00:00:00', output)
        self.assertIn(('run', 'view', '77', '--job', '1', '--log-failed'), self.calls)
        self.assertNotIn(('run', 'view', '77', '--job', '2', '--log-failed'), self.calls)

    def test_flaky_and_failed_lines_are_kept_even_when_they_are_above_the_tail(self):
        self.conclusion = 'failure'
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'failure'}]
        lines = ['FLAKY com.elarni.AndroidVpnE2E#joins: failed, then passed when rerun alone',
                 '--- FAIL: TestPathChange (5.55s)'] + [f'noise {number}' for number in range(100)]
        self.log = ''.join(f'job\tstep\t2026-11-16T00:00:00.0000000Z {line}\n' for line in lines)
        code, output = self.run_main('77', '--lines', '5')
        self.assertEqual(code, 1)
        self.assertIn('FLAKY com.elarni.AndroidVpnE2E#joins', output)
        self.assertIn('--- FAIL: TestPathChange', output)
        self.assertNotIn('noise 10\n', output)

    def test_the_log_ends_at_the_failed_command_not_at_the_cleanup_after_it(self):
        self.conclusion = 'failure'
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'failure'}]
        lines = (['the test output', '##[error]Process completed with exit code 1.'] +
                 [f'[command]/usr/bin/git config cleanup {number}' for number in range(30)])
        self.log = ''.join(f'job\tUNKNOWN STEP\t2026-11-16T00:00:00.0000000Z {line}\n' for line in lines)
        code, output = self.run_main('77', '--lines', '5')
        self.assertEqual(code, 1)
        self.assertIn('the test output', output)
        self.assertIn('Process completed with exit code 1', output)
        self.assertNotIn('git config cleanup', output)

    def test_a_line_check_android_repeats_per_emulator_is_printed_once_with_the_emulator(self):
        self.conclusion = 'failure'
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'failure'}]
        flaky = 'FLAKY com.elarni.AndroidVpnE2E#joins: failed, then passed when rerun alone'
        lines = [flaky, '  Elarni_API_30 emulator-5556: ' + flaky] + [f'noise {number}' for number in range(50)]
        self.log = ''.join(f'job\tstep\t2026-11-16T00:00:00.0000000Z {line}\n' for line in lines)
        _, output = self.run_main('77', '--lines', '5')
        self.assertEqual(output.count('FLAKY com.elarni.AndroidVpnE2E#joins'), 1)
        self.assertIn('Elarni_API_30 emulator-5556: FLAKY', output)

    def test_without_a_run_id_the_newest_run_of_the_branch_is_waited_for(self):
        code, _ = self.run_main('--branch', 'fix/thing')
        self.assertEqual(code, 0)
        listing = next(call for call in self.calls if call[:2] == ('run', 'list'))
        self.assertIn('fix/thing', listing)
        self.assertIn(('run', 'watch', '77', '--exit-status', '--interval', '30'), self.calls)

    def test_the_event_filter_picks_the_run_of_that_event(self):
        self.runs = [{'databaseId': 78, 'event': 'workflow_dispatch', 'status': 'queued', 'headSha': 'a' * 40},
                     {'databaseId': 77, 'event': 'pull_request', 'status': 'in_progress', 'headSha': 'a' * 40}]
        self.run_main('--branch', 'fix/thing', '--event', 'pull_request')
        self.assertIn(('run', 'watch', '77', '--exit-status', '--interval', '30'), self.calls)

    def test_a_branch_without_a_run_exits_two_and_waits_for_nothing(self):
        self.runs = []
        code, output = self.run_main('--branch', 'fix/thing')
        self.assertEqual(code, 2)
        self.assertIn('No run', output)
        self.assertFalse(any(call[:2] == ('run', 'watch') for call in self.calls))

    def test_a_cancelled_run_is_not_reported_as_passed(self):
        self.conclusion = 'cancelled'
        self.jobs = [{'databaseId': 1, 'name': 'linux checks and release', 'conclusion': 'cancelled'}]
        code, output = self.run_main('77')
        self.assertEqual(code, 1)
        self.assertIn('cancelled', output)

    def queue(self, name, *labels):
        self.queued.append({'name': name, 'status': 'queued', 'labels': list(labels)})

    def offline(self, name, offline=True):
        next(runner for runner in self.runners if runner['name'] == name)['status'] = 'offline' if offline else 'online'

    def test_a_job_queued_for_an_offline_runner_ends_the_wait_with_exit_three(self):
        self.queue('linux checks and release', 'self-hosted', 'elarni-linux')
        self.offline('linux-server')
        self.watch_timeouts = [lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 3)
        self.assertIn('linux-server', output)
        self.assertIn('linux checks and release', output)
        self.assertIn('offline', output)
        self.assertEqual(sum(call[:2] == ('run', 'watch') for call in self.calls), 1)

    def test_a_runner_that_comes_back_before_the_next_look_does_not_end_the_wait(self):
        self.queue('linux checks and release', 'self-hosted', 'elarni-linux')
        self.offline('linux-server')
        self.watch_timeouts = [lambda: self.offline('linux-server', False)]
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertIn('linux-server', output)
        self.assertIn('success  linux checks and release', output)

    def test_a_runner_that_goes_offline_during_the_wait_is_seen_at_a_later_look(self):
        self.queue('windows-2022 checks and release', 'self-hosted', 'elarni-windows')
        self.watch_timeouts = [lambda: self.offline('windows-vm'), lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 3)
        self.assertIn('windows-vm', output)

    def test_a_job_queued_for_a_busy_or_hosted_runner_is_waited_for(self):
        self.queue('linux checks and release', 'self-hosted', 'elarni-linux')
        self.queue('macos-15 checks', 'macos-15')
        self.offline('windows-vm')
        self.watch_timeouts = [lambda: None, lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertNotIn('offline', output)

    def test_a_job_that_another_online_runner_can_take_is_waited_for(self):
        self.runners.append({'name': 'linux-second', 'status': 'online', 'labels': [{'name': 'self-hosted'}, {'name': 'elarni-linux'}]})
        self.queue('linux checks and release', 'self-hosted', 'elarni-linux')
        self.offline('linux-server')
        self.watch_timeouts = [lambda: None, lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertNotIn('offline', output)

    def test_a_job_that_is_running_on_a_runner_gone_offline_is_left_to_github(self):
        self.queued.append({'name': 'linux checks and release', 'status': 'in_progress', 'labels': ['self-hosted', 'elarni-linux']})
        self.offline('linux-server')
        self.watch_timeouts = [lambda: None, lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertNotIn('offline', output)

    def test_an_account_that_may_not_list_runners_waits_as_before(self):
        self.queue('linux checks and release', 'self-hosted', 'elarni-linux')
        self.offline('linux-server')
        self.runners_refused = True
        self.watch_timeouts = [lambda: None, lambda: None]
        code, output = self.run_main('77')
        self.assertEqual(code, 0)
        self.assertNotIn('offline', output)


if __name__ == '__main__':
    unittest.main()
