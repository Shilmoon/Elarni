"""Check a downloaded draft release before the publish stage makes it public.

A draft is accepted only when it is one build's whole set of files: every file is in SHA256SUMS, every
package has build provenance from the release workflow at the draft's commit and from the same run, nothing
that run attested is left out, the tag names the version it built, and the signed APK holds exactly what
the run's unsigned APK holds.
"""
import argparse
import fnmatch
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

WORKFLOW = '.github/workflows/release.yml'
# The artifact of that workflow's build stage which holds the unsigned APK.
APK_ARTIFACT = 'unsigned-candidate-linux'
# One attested file of each kind makes a build; the provenance job of the workflow lists the same files.
# The inventory itself is the subject list of the build's attestation, which names them all.
KINDS = ('*_amd64.deb', '*.AppImage', '*-linux-x64.tar.gz', '*.msi', '*.zip', '*-project-source.tar.gz',
         '*go-module-sources.tar.gz', '*.apk')
UNLISTED = ('SHA256SUMS', 'SHA256SUMS.sig')
# The publish stage's own signature, there already when a publish that failed after its upload runs again.
PUBLISHED = 'SHA256SUMS.sigstore.json'
# What signing adds to an APK besides the signing block, which is not an entry.
SIGNATURE = re.compile(r'META-INF/(MANIFEST\.MF|[^/]+\.(SF|RSA|DSA|EC))')


class DraftError(Exception):
    pass


def sha256(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as file:
        for block in iter(lambda: file.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def check_draft(directory, tag, commit, verify, unsigned_apk):
    """Raise DraftError unless the files in `directory` are one build's release.

    verify(path) returns, for each provenance attestation of the file from the release workflow at `commit`,
    {'run': the run that made it, 'subjects': {name: sha256} of everything that attestation names}.
    unsigned_apk(run, name, sha256) returns the path of that run's unsigned APK.
    """
    directory = Path(directory)
    if not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise DraftError(f"the draft's target is {commit!r}, not the commit that was built")
    files = sorted(path.name for path in directory.iterdir() if path.is_file() and path.name != PUBLISHED)
    for name in UNLISTED:
        if name not in files or not (directory / name).stat().st_size:
            raise DraftError(f'{name} is missing or empty')

    listed = {}
    for line in (directory / 'SHA256SUMS').read_text().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64}) [ *](.+)', line.rstrip('\r'))
        if not match or match[2] in listed:
            raise DraftError(f'SHA256SUMS has a line that is not one file with its digest: {line!r}')
        listed[match[2]] = match[1]
    digests = {name: sha256(directory / name) for name in files if name not in UNLISTED}
    for name in digests:
        if name not in listed:
            raise DraftError(f'{name} is not in SHA256SUMS')
        if listed[name] != digests[name]:
            raise DraftError(f'{name} does not match SHA256SUMS')
    for name in listed:
        if name not in digests:
            raise DraftError(f'{name} is in SHA256SUMS but not in the draft')

    apks = [name for name in digests if name.endswith('.apk')]
    if len(apks) != 1:
        raise DraftError(f'the draft must hold one APK, not {len(apks)}')
    builds = {}
    for name in digests:
        if name in apks or name.endswith('.intoto.jsonl'):
            continue
        builds[name] = {found['run']: found['subjects'] for found in verify(directory / name)}
        if not builds[name]:
            raise DraftError(f'{name} has no build provenance from {WORKFLOW} at {commit}')
    common = set.intersection(*(set(runs) for runs in builds.values())) if builds else set()
    if not common:
        raise DraftError('the files are not from one build: ' +
                         ', '.join(f'{name} from {" or ".join(sorted(runs))}' for name, runs in builds.items()))
    run = sorted(common)[-1]
    subjects = {}
    for runs in builds.values():
        subjects.update(runs[run])

    for kind in KINDS:
        if len(fnmatch.filter(subjects, kind)) != 1:
            raise DraftError(f'the build must attest one {kind} file; it names {", ".join(sorted(subjects))}')
    if len(subjects) != len(KINDS):
        raise DraftError(f'the build attests a file of no known kind: {", ".join(sorted(subjects))}')
    built_apk = fnmatch.filter(subjects, '*.apk')[0]
    present = set(digests.values())
    for name, digest in sorted(subjects.items()):
        if name != built_apk and digest not in present:
            raise DraftError(f'{name} is missing: the build made it and the draft does not hold it')
    package = re.fullmatch(r'elarni_(.+)-(\d+)_amd64\.deb', fnmatch.filter(subjects, '*_amd64.deb')[0])
    if not package or tag != 'v' + package[1]:
        raise DraftError(f'the tag is {tag}, and the build is {fnmatch.filter(subjects, "*_amd64.deb")[0]}')

    unsigned = unsigned_apk(run, built_apk, subjects[built_apk])
    if sha256(unsigned) != subjects[built_apk]:
        raise DraftError(f'the unsigned APK of {run} is not the one its provenance names')
    if digests[apks[0]] == subjects[built_apk]:
        raise DraftError(f'{apks[0]} is the unsigned APK: it is not signed')
    compare_apks(unsigned, directory / apks[0])
    return {'run': run, 'version': package[1], 'build': package[2], 'files': len(files)}


def compare_apks(unsigned, signed):
    """Signing may add signature entries; every other entry must be the build's, byte for byte."""
    def entries(path):
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
            if len(names) != len(set(names)):
                raise DraftError(f'{Path(path).name} has two entries of one name')
            return {name: hashlib.sha256(archive.read(name)).hexdigest()
                    for name in names if not SIGNATURE.fullmatch(name)}
    built, shipped = entries(unsigned), entries(signed)
    different = sorted(name for name in built.keys() | shipped.keys() if built.get(name) != shipped.get(name))
    if different:
        raise DraftError(f'{Path(signed).name} differs from the unsigned APK of the build in ' + ', '.join(different[:20]))


def gh(*arguments):
    result = subprocess.run(['gh', *arguments], capture_output=True, text=True)
    if result.returncode:
        raise DraftError(f'gh {" ".join(arguments[:3])} failed: {result.stderr.strip()}')
    return result.stdout


def attestations(repository, commit):
    def verify(path):
        result = subprocess.run(
            ['gh', 'attestation', 'verify', str(path), '--repo', repository,
             '--signer-workflow', f'{repository}/{WORKFLOW}', '--source-digest', commit,
             '--deny-self-hosted-runners', '--format', 'json'], capture_output=True, text=True)
        if result.returncode:
            print(result.stderr.strip(), file=sys.stderr)
            return []
        found = []
        for entry in json.loads(result.stdout):
            verified = entry['verificationResult']
            # The certificate's fields come from GitHub's token, not from the workflow's own steps.
            certificate = verified['signature']['certificate']
            if certificate['sourceRepositoryDigest'] != commit:
                continue
            found.append({'run': certificate['runInvocationURI'],
                          'subjects': {subject['name']: subject['digest']['sha256']
                                       for subject in verified['statement']['subject']}})
        return found
    return verify


def run_artifact(repository, scratch):
    def unsigned_apk(run, name, digest):
        match = re.search(r'/actions/runs/(\d+)/attempts/\d+$', run)
        if not match:
            raise DraftError(f'{run} does not name a workflow run')
        gh('run', 'download', match[1], '--repo', repository, '--name', APK_ARTIFACT, '--dir', str(scratch))
        for path in sorted(Path(scratch).rglob('*.apk')):
            if sha256(path) == digest:
                return path
        raise DraftError(f'{APK_ARTIFACT} of {run} holds no unsigned APK with the digest its provenance names for {name}')
    return unsigned_apk


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument('directory', help='the downloaded files of the draft')
    parser.add_argument('--repo', required=True, help='owner/name of the repository')
    parser.add_argument('--tag', required=True, help='the tag of the draft, such as v0.4.1')
    parser.add_argument('--commit', required=True, help="the draft's target commit")
    arguments = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='elarni-unsigned-') as scratch:
        try:
            build = check_draft(arguments.directory, arguments.tag, arguments.commit,
                                attestations(arguments.repo, arguments.commit), run_artifact(arguments.repo, scratch))
        except DraftError as error:
            print(f'REFUSED: {error}', file=sys.stderr)
            return 1
    print(f"{build['files']} files of Elarni {build['version']} (build {build['build']}) from {build['run']}")
    return 0


if __name__ == '__main__':
    sys.exit(main())
