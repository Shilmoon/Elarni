"""What the publish stage accepts as a draft release: one build's whole set of files, and nothing else."""
import hashlib
from pathlib import Path
import sys
import tempfile
import unittest
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
from release_draft import DraftError, check_draft  # noqa: E402

COMMIT = '54a46f71cfb8a880a292cbcabb7ab9b660e2b7e1'
RUN = 'https://github.com/example/elarni/actions/runs/7/attempts/1'
OTHER_RUN = 'https://github.com/example/elarni/actions/runs/6/attempts/1'
# The names the build attests, and the names the release machine gives two of them.
BUILT = ['elarni_0.7.0-17_amd64.deb', 'Elarni-0.7.0-17-x86_64.AppImage', 'elarni-0.7.0-17-linux-x64.tar.gz',
         'elarni-0.7.0-17-linux-bundled-sources.tar',
         'Elarni-0.7.0-17-windows-x64-UNSIGNED.msi', 'Elarni-0.7.0-17-windows-x64-UNSIGNED.zip',
         'Elarni-0.7.0-17-project-source.tar.gz', 'elarni-go-module-sources.tar.gz']
RENAMED = {'elarni-go-module-sources.tar.gz': 'Elarni-0.7.0-17-go-module-sources.tar.gz'}
UNSIGNED, SIGNED = 'app-release-unsigned.apk', 'Elarni-0.7.0-17-android-arm64.apk'


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def apk(path, entries):
    with zipfile.ZipFile(path, 'w') as archive:
        for name, data in entries.items():
            archive.writestr(name, data)


class Draft(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='elarni-draft-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.draft, self.build = self.root / 'draft', self.root / 'build'
        self.draft.mkdir()
        self.build.mkdir()
        self.subjects = {}
        for name in BUILT:
            (self.draft / RENAMED.get(name, name)).write_text(name)
            self.subjects[name] = digest(self.draft / RENAMED.get(name, name))
        self.code = {'classes.dex': 'code', 'lib/arm64-v8a/libxray.so': 'core', 'AndroidManifest.xml': 'manifest'}
        apk(self.build / UNSIGNED, self.code)
        self.subjects[UNSIGNED] = digest(self.build / UNSIGNED)
        apk(self.draft / SIGNED, {**self.code, 'META-INF/CERT.SF': 'signature', 'META-INF/CERT.RSA': 'key',
                                  'META-INF/MANIFEST.MF': 'digests'})
        (self.draft / 'Elarni-0.7.0-17.intoto.jsonl').write_text('{}\n')
        (self.draft / 'SHA256SUMS.sig').write_bytes(b'signature')
        # Per file digest: the builds whose provenance GitHub holds for it.
        self.runs = {value: [RUN] for value in self.subjects.values()}
        self.other = dict(self.subjects)
        self.fetched = []
        self.sums()

    def sums(self, skip=()):
        lines = [f'{digest(path)}  {path.name}' for path in sorted(self.draft.iterdir())
                 if path.name not in ('SHA256SUMS', 'SHA256SUMS.sig', *skip)]
        (self.draft / 'SHA256SUMS').write_text('\n'.join(lines) + '\n')

    def verify(self, path):
        return [{'run': run, 'subjects': self.subjects if run == RUN else self.other}
                for run in self.runs.get(digest(path), [])]

    def unsigned(self, run, name, sha256):
        self.fetched.append((run, name, sha256))
        return self.build / UNSIGNED

    def check(self, tag='v0.7.0', commit=COMMIT):
        return check_draft(self.draft, tag, commit, self.verify, self.unsigned)

    def refused(self, text, **arguments):
        with self.assertRaises(DraftError) as raised:
            self.check(**arguments)
        self.assertIn(text, str(raised.exception))

    def test_one_build_with_every_file_passes(self):
        self.assertEqual(self.check(), {'run': RUN, 'version': '0.7.0', 'build': '17', 'files': 12})
        self.assertEqual(self.fetched, [(RUN, UNSIGNED, self.subjects[UNSIGNED])])

    def test_a_publish_that_stopped_after_its_upload_can_run_again(self):
        (self.draft / 'SHA256SUMS.sigstore.json').write_text('{}')
        self.assertEqual(self.check()['files'], 12)

    def test_a_package_the_build_made_must_be_in_the_draft(self):
        (self.draft / 'Elarni-0.7.0-17-windows-x64-UNSIGNED.msi').unlink()
        self.sums()
        self.refused('Elarni-0.7.0-17-windows-x64-UNSIGNED.msi is missing')

    def test_a_file_from_another_build_is_refused(self):
        # The same workflow built it, so its provenance is valid on its own.
        stale = self.draft / 'Elarni-0.7.0-17-x86_64.AppImage'
        stale.write_text('an earlier build')
        self.other = {**self.subjects, 'Elarni-0.7.0-17-x86_64.AppImage': digest(stale)}
        self.runs[digest(stale)] = [OTHER_RUN]
        self.sums()
        self.refused('not from one build')

    def test_a_file_without_provenance_at_the_commit_is_refused(self):
        (self.draft / 'notes.txt').write_text('extra')
        self.sums()
        self.refused('notes.txt has no build provenance')

    def test_every_file_must_be_in_the_checksums_and_match_them(self):
        self.sums(skip=['Elarni-0.7.0-17-x86_64.AppImage'])
        self.refused('Elarni-0.7.0-17-x86_64.AppImage is not in SHA256SUMS')
        self.sums()
        (self.draft / 'Elarni-0.7.0-17.intoto.jsonl').write_text('changed\n')
        self.refused('Elarni-0.7.0-17.intoto.jsonl does not match SHA256SUMS')
        self.sums()
        (self.draft / 'Elarni-0.7.0-17.intoto.jsonl').unlink()
        self.refused('Elarni-0.7.0-17.intoto.jsonl is in SHA256SUMS but not in the draft')

    def test_the_release_key_signature_is_required(self):
        (self.draft / 'SHA256SUMS.sig').write_bytes(b'')
        self.refused('SHA256SUMS.sig')

    def test_the_tag_names_the_version_that_was_built(self):
        self.refused('v0.7.1', tag='v0.7.1')

    def test_the_draft_points_at_a_commit(self):
        self.refused('main', commit='main')

    def test_the_inventory_is_the_whole_list_of_package_kinds(self):
        # An attestation per file would hide a file left out of the draft.
        del self.subjects['Elarni-0.7.0-17-windows-x64-UNSIGNED.msi']
        (self.draft / 'Elarni-0.7.0-17-windows-x64-UNSIGNED.msi').unlink()
        self.sums()
        self.refused('.msi')

    def test_the_signed_apk_holds_what_the_build_made(self):
        apk(self.draft / SIGNED, {**self.code, 'classes.dex': 'other code', 'META-INF/CERT.SF': 'signature'})
        self.sums()
        self.refused('classes.dex')
        apk(self.draft / SIGNED, {**self.code, 'assets/extra.bin': 'added', 'META-INF/CERT.SF': 'signature'})
        self.sums()
        self.refused('assets/extra.bin')
        without = dict(self.code)
        del without['lib/arm64-v8a/libxray.so']
        apk(self.draft / SIGNED, {**without, 'META-INF/CERT.SF': 'signature'})
        self.sums()
        self.refused('lib/arm64-v8a/libxray.so')

    def test_the_unsigned_apk_compared_is_the_attested_one(self):
        apk(self.build / UNSIGNED, {**self.code, 'classes.dex': 'other code'})
        self.refused('unsigned APK')

    def test_the_apk_must_be_signed_and_alone(self):
        (self.draft / SIGNED).write_bytes((self.build / UNSIGNED).read_bytes())
        self.sums()
        self.refused('not signed')
        (self.draft / 'second.apk').write_bytes((self.draft / SIGNED).read_bytes())
        self.sums()
        self.refused('one APK')


if __name__ == '__main__':
    unittest.main()
