"""Exercise check-linux-container's turn-taking through a fake docker in a scratch repository."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent
# Stands in for docker: records each call, and a file named "held" in its state folder is a container that holds the name.
FAKE_DOCKER = r'''#!/usr/bin/env python3
import os, sys, time
from pathlib import Path
state, args = Path(os.environ['FAKE_DOCKER_STATE']), sys.argv[1:]
with (state / 'calls').open('a') as calls:
    calls.write(args[0] + '\n')
held = state / 'held'
if args[0] == 'ps':
    # The leftover query asks for CI-labelled containers; the holder here is a manual run, which has no label.
    if 'label=elarni.ci' not in args and held.exists():
        print('0123456789ab')
elif args[0] == 'wait':
    if (state / 'never-ends').exists():
        time.sleep(30)
    held.unlink(missing_ok=True)
elif args[0] == 'run':
    if held.exists():
        sys.exit(125)
    if (state / 'taken-once').exists():
        (state / 'taken-once').unlink()
        held.touch()
        sys.exit(125)
'''


@unittest.skipIf(os.name == 'nt', 'the fake docker is a POSIX executable')
class ContainerTurns(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-container-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve() / 'repo'
        (self.root / 'scripts').mkdir(parents=True)
        for name in ('check-linux-container', 'snapshot.py'):
            shutil.copy(SCRIPTS / name, self.root / 'scripts' / name)
        self.state = self.root.parent / 'state'
        self.state.mkdir()
        tools = self.root.parent / 'tools'
        tools.mkdir()
        (tools / 'docker').write_text(FAKE_DOCKER)
        (tools / 'docker').chmod(0o755)
        self.path = f'{tools}{os.pathsep}{os.environ["PATH"]}'
        for command in (['init', '-q'], ['add', '-A'], ['commit', '-qm', 'fixture']):
            subprocess.run(['git', '-c', 'user.name=t', '-c', 'user.email=t@t', *command], cwd=self.root, check=True, capture_output=True)

    def check(self, ci=True, wait_minutes=None):
        env = {key: value for key, value in os.environ.items() if key not in ('GITHUB_ACTIONS', 'ELARNI_LINUX_HOST')}
        env.update(PATH=self.path, FAKE_DOCKER_STATE=str(self.state))
        if ci:
            env['GITHUB_ACTIONS'] = 'true'
        if wait_minutes is not None:
            env['ELARNI_LINUX_WAIT_MINUTES'] = str(wait_minutes)
        result = subprocess.run([sys.executable, str(self.root / 'scripts/check-linux-container'), '--', 'true'],
                                cwd=self.root, env=env, capture_output=True, text=True, timeout=20)
        calls = (self.state / 'calls').read_text().split()
        return result, [call for call in calls if call in ('wait', 'run')]

    def test_a_free_name_runs_at_once(self):
        result, calls = self.check()
        self.assertEqual((result.returncode, calls), (0, ['run']), result.stdout + result.stderr)

    def test_a_ci_run_waits_for_a_manual_run_to_end(self):
        (self.state / 'held').touch()
        result, calls = self.check()
        self.assertEqual((result.returncode, calls), (0, ['wait', 'run']), result.stdout + result.stderr)
        self.assertIn('waiting', result.stdout)

    def test_a_ci_run_waits_again_when_the_name_is_taken_before_it_starts(self):
        (self.state / 'taken-once').touch()
        result, calls = self.check()
        self.assertEqual((result.returncode, calls), (0, ['run', 'wait', 'run']), result.stdout + result.stderr)

    def test_a_ci_run_gives_up_when_the_name_stays_held(self):
        (self.state / 'held').touch()
        (self.state / 'never-ends').touch()
        result, calls = self.check(wait_minutes=0.02)
        self.assertEqual((result.returncode, calls), (125, ['wait']), result.stdout + result.stderr)
        self.assertIn('still holds', result.stdout)

    def test_a_manual_run_is_refused_without_waiting(self):
        (self.state / 'held').touch()
        result, calls = self.check(ci=False)
        self.assertEqual((result.returncode, calls), (125, ['run']), result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
