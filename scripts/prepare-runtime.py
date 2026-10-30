#!/usr/bin/env python3
"""Stage locked official runtimes for one desktop target without using host tools."""

import argparse
import hashlib
import json
import os
import posixpath
import shutil
import stat
import tarfile
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path, PurePosixPath


REPO = Path(__file__).resolve().parents[1]
LOCK_FILE = REPO / "build" / "runtime-lock.json"
NOTICE_FILE = REPO / "build" / "runtime" / "THIRD-PARTY-NOTICES.md"
SOURCE_FILE = REPO / "build" / "runtime" / "SOURCE-CODE.md"
CLI_NODE_MODULES = REPO / "build" / "runtime" / "cli" / "node_modules"
PLATFORMS = ("darwin-arm64", "darwin-amd64", "windows-amd64")


class PrepareError(RuntimeError):
    pass


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def artifact_path(component, platform, cache_dir):
    record = component["archives"][platform]
    filename = Path(urllib.parse.urlparse(record["url"]).path).name
    if not filename:
        raise PrepareError("locked artifact URL has no filename")
    target = cache_dir / filename
    target.parent.mkdir(parents=True, exist_ok=True)

    if target.exists() and sha256(target) != record["sha256"]:
        target.unlink()

    if not target.exists():
        print("Downloading", record["url"])
        request = urllib.request.Request(
            record["url"], headers={"User-Agent": "SkillManager-runtime-preparer/1"}
        )
        temporary = target.with_suffix(target.suffix + ".part")
        try:
            with urllib.request.urlopen(request, timeout=90) as response:
                with temporary.open("wb") as destination:
                    shutil.copyfileobj(response, destination, length=1024 * 1024)
            actual = sha256(temporary)
            if actual != record["sha256"]:
                raise PrepareError(
                    "SHA-256 mismatch for {}: expected {}, got {}".format(
                        target.name, record["sha256"], actual
                    )
                )
            os.replace(str(temporary), str(target))
        finally:
            if temporary.exists():
                temporary.unlink()

    actual = sha256(target)
    if actual != record["sha256"]:
        raise PrepareError(
            "cached SHA-256 mismatch for {}: expected {}, got {}".format(
                target.name, record["sha256"], actual
            )
        )
    print("Verified SHA-256", actual, target.name)
    return target


def normalized_member_name(name):
    if "\\" in name:
        raise PrepareError("archive entry contains a Windows path separator: " + name)
    path = PurePosixPath(name)
    if path.is_absolute() or any(part == ".." for part in path.parts):
        raise PrepareError("unsafe archive path: " + name)
    parts = tuple(part for part in path.parts if part not in ("", "."))
    return "/".join(parts)


def checked_link_target(member_name, target, hardlink=False):
    if "\\" in target:
        raise PrepareError("archive link contains a Windows path separator: " + target)
    link = PurePosixPath(target)
    if link.is_absolute():
        raise PrepareError("absolute archive link is not allowed: " + target)
    if hardlink:
        candidate = posixpath.normpath(target)
    else:
        candidate = posixpath.normpath(
            posixpath.join(posixpath.dirname(member_name), target)
        )
    if candidate == ".." or candidate.startswith("../"):
        raise PrepareError("archive link escapes its output directory: " + target)


def strip_root_prefix(names, expected_prefix=None):
    cleaned = [normalized_member_name(name) for name in names]
    nonempty = [name for name in cleaned if name]
    if not nonempty:
        return [""] * len(cleaned)

    if expected_prefix:
        if not all(name == expected_prefix or name.startswith(expected_prefix + "/") for name in nonempty):
            raise PrepareError("archive did not contain expected top directory " + expected_prefix)
        return [name[len(expected_prefix) :].lstrip("/") if name else "" for name in cleaned]

    first = nonempty[0].split("/", 1)[0]
    if all(name == first or name.startswith(first + "/") for name in nonempty):
        return [name[len(first) :].lstrip("/") if name else "" for name in cleaned]
    return cleaned


def unpack_tar(archive_path, destination, expected_prefix=None):
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(str(archive_path), mode="r:*") as archive:
        members = archive.getmembers()
        names = strip_root_prefix([member.name for member in members], expected_prefix)
        safe_members = []
        for member, name in zip(members, names):
            if not name:
                continue
            member.name = name
            if member.issym():
                checked_link_target(name, member.linkname)
            elif member.islnk():
                checked_link_target(name, member.linkname, hardlink=True)
            elif not (member.isdir() or member.isfile()):
                raise PrepareError("unsupported special file in archive: " + name)
            safe_members.append(member)
        archive.extractall(str(destination), members=safe_members)


def unpack_zip(archive_path, destination, expected_prefix=None):
    destination.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(str(archive_path)) as archive:
        infos = archive.infolist()
        names = strip_root_prefix([info.filename for info in infos], expected_prefix)
        for info, name in zip(infos, names):
            if not name:
                continue
            output = destination.joinpath(*PurePosixPath(name).parts)
            if not output.resolve().is_relative_to(destination.resolve()):
                raise PrepareError("ZIP entry escapes extraction directory: " + info.filename)
            mode = info.external_attr >> 16
            if stat.S_ISLNK(mode):
                target = archive.read(info).decode("utf-8")
                checked_link_target(name, target)
                output.parent.mkdir(parents=True, exist_ok=True)
                output.symlink_to(target)
            elif info.is_dir():
                output.mkdir(parents=True, exist_ok=True)
            else:
                output.parent.mkdir(parents=True, exist_ok=True)
                with archive.open(info) as source, output.open("wb") as destination_file:
                    shutil.copyfileobj(source, destination_file, length=1024 * 1024)
                permissions = mode & 0o777
                if permissions:
                    output.chmod(permissions)


def extract(component, platform, cache_dir, destination):
    artifact = artifact_path(component, platform, cache_dir)
    archive_type = component["archives"][platform]["archive"]
    if archive_type in ("tar.xz", "tar.gz"):
        prefix = None
        if component is NODE and platform.startswith("darwin-"):
            prefix = "node-v{}-{}".format(
                NODE["version"], "darwin-arm64" if platform.endswith("arm64") else "darwin-x64"
            )
        elif component is NODE and platform == "windows-amd64":
            prefix = "node-v{}-win-x64".format(NODE["version"])
        unpack_tar(artifact, destination, prefix)
    elif archive_type == "zip":
        prefix = "node-v{}-win-x64".format(NODE["version"])
        unpack_zip(artifact, destination, prefix)
    else:
        raise PrepareError("unsupported archive type: " + archive_type)


def npm_directory(node_distribution, platform):
    if platform.startswith("darwin-"):
        return node_distribution / "lib" / "node_modules" / "npm"
    return node_distribution / "node_modules" / "npm"


def copy_npm(node_distribution, platform, stage):
    """Copy npm's JavaScript package from the verified Node build input only."""
    source = npm_directory(node_distribution, platform)
    required(source / "bin" / "npx-cli.js", "npm npx CLI")
    required(source / "bin" / "npm-cli.js", "npm CLI")
    required(source / "LICENSE", "npm license")
    required(source / "package.json", "npm package metadata")
    shutil.copytree(
        str(source),
        str(stage / "npm"),
        symlinks=True,
    )


def required(path, description):
    if not path.exists():
        raise PrepareError("expected staged {} at {}".format(description, path))
    return path


def optional_existing(root, candidates):
    for relative in candidates:
        if (root / relative).exists():
            return relative
    return ""


def copy_cli(stage):
    required(CLI_NODE_MODULES / "skills" / "dist" / "cli.mjs", "pinned Skills CLI")
    required(CLI_NODE_MODULES / "tar" / "package.json", "locked tar dependency")
    required(CLI_NODE_MODULES / "yaml" / "package.json", "locked yaml dependency")
    shutil.copytree(
        str(CLI_NODE_MODULES),
        str(stage / "skills" / "node_modules"),
        symlinks=True,
        ignore=shutil.ignore_patterns(".package-lock.json"),
    )


def make_manifest(stage, platform, lock):
    if platform.startswith("darwin-"):
        npx_cli = "npm/bin/npx-cli.js"
        git = "git/bin/git"
        git_exec = "git/libexec/git-core"
        paths = ["git/bin"]
        config_candidates = ["git/etc/gitconfig"]
        template_candidates = ["git/share/git-core/templates"]
        ca_candidates = ["git/ssl/cacert.pem", "git/etc/ssl/certs/ca-bundle.crt"]
    else:
        npx_cli = "npm/bin/npx-cli.js"
        git = "git/cmd/git.exe"
        git_exec = "git/ucrt64/libexec/git-core"
        paths = ["git/cmd", "git/ucrt64/bin", "git/usr/bin"]
        config_candidates = ["git/etc/gitconfig", "git/ucrt64/etc/gitconfig"]
        template_candidates = ["git/ucrt64/share/git-core/templates"]
        ca_candidates = [
            "git/ucrt64/ssl/certs/ca-bundle.crt",
            "git/ucrt64/etc/ssl/certs/ca-bundle.crt",
            "git/usr/ssl/certs/ca-bundle.crt",
        ]

    for rel, description in ((npx_cli, "npm npx CLI"), (git, "Git executable"),
                             (git_exec, "Git exec path")):
        required(stage / rel, description)
    for rel in paths:
        required(stage / rel, "PATH directory")

    manifest = {
        "schemaVersion": 2,
        "platform": platform,
        "nodeMode": "system",
        "npxCLI": npx_cli,
        "skillsCLI": "skills/node_modules/skills/dist/cli.mjs",
        "git": git,
        "gitExecPath": git_exec,
        "gitConfigSystem": optional_existing(stage, config_candidates),
        "gitTemplateDir": optional_existing(stage, template_candidates),
        "caBundle": optional_existing(stage, ca_candidates),
        "path": paths,
        "versions": {
            "node": "system",
            "npm": lock["node"]["npmVersion"],
            "git": lock["git"]["version"],
            "skills": lock["skills"]["version"],
        },
    }
    with (stage / "runtime.json").open("w", encoding="utf-8", newline="\n") as output:
        json.dump(manifest, output, indent=2)
        output.write("\n")


def copy_notices(stage, node_distribution):
    license_dir = stage / "licenses"
    license_dir.mkdir(parents=True, exist_ok=True)
    committed_licenses = REPO / "build" / "runtime" / "licenses"
    if committed_licenses.exists():
        shutil.copytree(
            str(committed_licenses), str(license_dir), symlinks=True, dirs_exist_ok=True
        )
    for source in (NOTICE_FILE, SOURCE_FILE):
        required(source, "distribution notice")
        shutil.copy2(str(source), str(license_dir / source.name))
    npm_notices = stage / "npm" / "LICENSE"
    required(npm_notices, "npm license")
    shutil.copy2(str(npm_notices), str(license_dir / "npm-LICENSE"))
    node_license = node_distribution / "LICENSE"
    required(node_license, "Node.js license")
    shutil.copy2(str(node_license), str(license_dir / "Node-LICENSE"))
    npm_modules = stage / "npm" / "node_modules"
    dependency_licenses = license_dir / "npm-node-modules"
    if npm_modules.exists():
        for package_json in npm_modules.rglob("package.json"):
            package_dir = package_json.parent
            if package_dir.is_symlink():
                continue
            relative_package = package_dir.relative_to(npm_modules)
            for pattern in ("LICENSE*", "LICENCE*", "COPYING*", "NOTICE*"):
                for notice in package_dir.glob(pattern):
                    if notice.is_file() and not notice.is_symlink():
                        destination = dependency_licenses / relative_package / notice.name
                        destination.parent.mkdir(parents=True, exist_ok=True)
                        shutil.copy2(str(notice), str(destination))
    skills_license = stage / "skills" / "node_modules" / "skills" / "LICENSE"
    required(skills_license, "Skills CLI license")
    shutil.copy2(str(skills_license), str(license_dir / "skills-LICENSE"))
    skills_package = stage / "skills" / "node_modules" / "skills"
    for name in ("THIRD-PARTY-LICENSES.md", "ThirdPartyNoticeText.txt"):
        source = skills_package / "dist" / name
        if not source.exists():
            source = skills_package / name
        required(source, "Skills CLI third-party notice " + name)
        shutil.copy2(str(source), str(license_dir / name))
    dependency_licenses = license_dir / "skills-node-modules"
    for package_json in (stage / "skills" / "node_modules").rglob("package.json"):
        package_dir = package_json.parent
        relative_package = package_dir.relative_to(stage / "skills" / "node_modules")
        for pattern in ("LICENSE*", "LICENCE*", "COPYING*", "NOTICE*"):
            for notice in package_dir.glob(pattern):
                if notice.is_file() and not notice.is_symlink():
                    destination = dependency_licenses / relative_package / notice.name
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copy2(str(notice), str(destination))


NODE = None


def prepare(platform, output_dir, cache_dir):
    global NODE
    with LOCK_FILE.open("r", encoding="utf-8") as source:
        lock = json.load(source)
    if lock.get("schemaVersion") != 1:
        raise PrepareError("unsupported runtime lock schema")
    if platform not in PLATFORMS:
        raise PrepareError("unsupported platform: " + platform)
    NODE = lock["node"]
    output_dir.mkdir(parents=True, exist_ok=True)
    cache_dir.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="runtime-{}-".format(platform), dir=str(output_dir)) as temp:
        stage = Path(temp) / platform
        stage.mkdir()
        (stage / "git").mkdir()
        node_distribution = Path(temp) / "node-build-input"
        extract(lock["node"], platform, cache_dir, node_distribution)
        extract(lock["git"], platform, cache_dir, stage / "git")
        copy_npm(node_distribution, platform, stage)
        copy_cli(stage)
        make_manifest(stage, platform, lock)
        copy_notices(stage, node_distribution)

        final = output_dir / platform
        backup = output_dir / (platform + ".old")
        if backup.exists():
            shutil.rmtree(str(backup))
        if final.exists():
            os.replace(str(final), str(backup))
        try:
            os.replace(str(stage), str(final))
        except Exception:
            if backup.exists() and not final.exists():
                os.replace(str(backup), str(final))
            raise
        if backup.exists():
            shutil.rmtree(str(backup))
    print("Staged", platform, "at", str(output_dir / platform))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", required=True, choices=PLATFORMS)
    parser.add_argument("--output", type=Path, default=REPO / "dist" / "runtime")
    parser.add_argument("--cache", type=Path, default=REPO / "dist" / "runtime-cache")
    args = parser.parse_args()
    try:
        prepare(args.platform, args.output.resolve(), args.cache.resolve())
    except (OSError, tarfile.TarError, zipfile.BadZipFile, urllib.error.URLError, PrepareError) as error:
        parser.exit(1, "runtime preparation failed: {}\n".format(error))


if __name__ == "__main__":
    main()
