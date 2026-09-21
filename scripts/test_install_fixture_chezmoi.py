import hashlib
import io
import tarfile
import unittest
import zipfile
import install_fixture_chezmoi as fixture


class FixtureTests(unittest.TestCase):
    def test_supported_native_runner_assets(self):
        self.assertEqual(fixture.asset_name("Darwin", "arm64"), "chezmoi_2.69.4_darwin_arm64.tar.gz")
        self.assertEqual(fixture.asset_name("Linux", "x86_64"), "chezmoi_2.69.4_linux_amd64.tar.gz")
        self.assertEqual(fixture.asset_name("Windows", "AMD64"), "chezmoi_2.69.4_windows_amd64.zip")

    def test_checksum_failure_and_regular_binary_only(self):
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w:gz") as archive:
            member = tarfile.TarInfo("chezmoi")
            member.size = 7
            archive.addfile(member, io.BytesIO(b"fixture"))
        raw = data.getvalue()
        name = fixture.asset_name("Linux", "amd64")
        checksum = hashlib.sha256(raw).hexdigest()
        self.assertEqual(fixture.verified_payload(name, f"{checksum}  {name}", raw), ("chezmoi", b"fixture"))
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            fixture.verified_payload(name, f"{'0' * 64}  {name}", raw)

    def test_windows_zip(self):
        data = io.BytesIO()
        with zipfile.ZipFile(data, "w") as archive:
            archive.writestr("chezmoi.exe", b"fixture")
        raw = data.getvalue()
        name = fixture.asset_name("Windows", "amd64")
        checksum = hashlib.sha256(raw).hexdigest()
        self.assertEqual(fixture.verified_payload(name, f"{checksum}  {name}", raw), ("chezmoi.exe", b"fixture"))


if __name__ == "__main__":
    unittest.main()
