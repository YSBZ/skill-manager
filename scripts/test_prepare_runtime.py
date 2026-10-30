import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("prepare_runtime", Path(__file__).with_name("prepare-runtime.py"))
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)


class MacCredentialPayloadTests(unittest.TestCase):
    def test_git_aliases_keep_content_and_resolve_inside_runtime(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "bin").mkdir()
            (root / "libexec/git-core").mkdir(parents=True)
            for name in ("git", "scalar"):
                (root / "bin" / name).write_bytes(b"same fixture binary")
                (root / "libexec/git-core" / name).write_bytes(b"same fixture binary")
            runtime.deduplicate_mac_git_aliases(root)
            for name in ("git", "scalar"):
                alias = root / "libexec/git-core" / name
                self.assertTrue(alias.is_symlink())
                self.assertEqual(alias.resolve(), (root / "bin" / name).resolve())
                self.assertEqual(alias.read_bytes(), b"same fixture binary")

    def test_remove_only_declared_optional_credential_payload(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            core = root / "libexec/git-core"
            core.mkdir(parents=True)
            for name in ("git", "git-lfs", "System.Private.CoreLib.dll", "libcoreclr.dylib", "git-credential-manager"):
                (core / name).write_bytes(b"fixture")
            (core / "git-credential-manager.deps.json").write_text(json.dumps({"targets": {"net/osx-arm64": {
                "runtime": {"runtime": {"System.Private.CoreLib.dll": {}}, "native": {"libcoreclr.dylib": {}}}
            }}}))
            runtime.trim_optional_mac_gcm(root)
            self.assertEqual({p.name for p in core.iterdir()}, {"git", "git-lfs"})

    def test_reject_dependency_map_that_names_git_itself(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            core = root / "libexec/git-core"
            core.mkdir(parents=True)
            (core / "git").write_bytes(b"keep")
            (core / "git-credential-manager.deps.json").write_text(json.dumps({"targets": {"net": {
                "bad": {"native": {"git": {}}}
            }}}))
            with self.assertRaises(runtime.PrepareError):
                runtime.trim_optional_mac_gcm(root)
            self.assertEqual((core / "git").read_bytes(), b"keep")


if __name__ == "__main__":
    unittest.main()
