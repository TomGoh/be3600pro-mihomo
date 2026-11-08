import contextlib
import hashlib
import io
import json
import pathlib
import sys
import tempfile
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parents[1]))
import core_release


class CoreReleaseTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = pathlib.Path(self.temp.name)
        self.archive = self.root / 'core.gz'
        self.patch = self.root / 'source.patch'
        self.manifest = self.root / 'release.json'
        self.archive.write_bytes(b'custom core fixture')
        self.patch.write_bytes(b'complete source patch fixture')
        self.value = {
            'schema': 1, 'version': 'v1.19.32-udpfix-candidate', 'goVersion': 'go1.26.8',
            'upstreamCommit': 'a' * 40,
            'archiveBytes': self.archive.stat().st_size,
            'archiveSha256': hashlib.sha256(self.archive.read_bytes()).hexdigest(),
            # Binary delta metadata must never be confused with source patch metadata.
            'patchBytes': 123456, 'patchSha256': '0' * 64,
            'sourcePatchBytes': self.patch.stat().st_size,
            'sourcePatchSha256': hashlib.sha256(self.patch.read_bytes()).hexdigest(),
        }

    def generate(self):
        self.manifest.write_text(json.dumps(self.value), encoding='utf-8')
        with contextlib.redirect_stdout(io.StringIO()):
            core_release.generate(self.manifest, self.archive, self.patch, self.root / 'assets', self.root / 'generated')

    def test_metadata_drives_compiled_upload_length_and_version(self):
        self.generate()
        source = (self.root / 'generated/dev/jose/routerproxy/CoreRelease.java').read_text()
        self.assertIn('ARCHIVE_BYTES=%dL' % len(self.archive.read_bytes()), source)
        self.assertIn(self.value['archiveSha256'], source)
        self.assertIn(self.value['version'], source)
        self.assertEqual((self.root / 'assets/core-release.json').read_bytes(), self.manifest.read_bytes())

    def test_source_patch_not_binary_delta_is_packaged(self):
        self.generate()
        self.assertEqual((self.root / 'assets' / core_release.PATCH_ASSET).read_bytes(), self.patch.read_bytes())
        self.assertIn(self.value['sourcePatchSha256'], (self.root / 'assets/CORE-SOURCE.txt').read_text())

    def test_same_length_corrupt_core_rejected(self):
        self.archive.write_bytes(b'X' * self.value['archiveBytes'])
        with self.assertRaises(ValueError):
            self.generate()

    def test_short_core_rejected(self):
        self.archive.write_bytes(b'short')
        with self.assertRaises(ValueError):
            self.generate()

    def test_corrupt_source_patch_rejected(self):
        self.patch.write_bytes(b'X' * self.value['sourcePatchBytes'])
        with self.assertRaises(ValueError):
            self.generate()

    def test_binary_delta_metadata_is_not_source_fallback(self):
        self.value.pop('sourcePatchBytes')
        self.value.pop('sourcePatchSha256')
        with self.assertRaises(ValueError):
            self.generate()

    def test_invalid_schema_and_version_rejected(self):
        self.value['schema'] = 2
        with self.assertRaises(ValueError):
            self.generate()
        self.value['schema'] = 1
        self.value['version'] = 'bad"; arbitrary Java source'
        with self.assertRaises(ValueError):
            self.generate()

    def test_manifest_size_must_be_integer(self):
        self.value['archiveBytes'] = str(self.value['archiveBytes'])
        with self.assertRaises(ValueError):
            self.generate()


if __name__ == '__main__':
    unittest.main()
