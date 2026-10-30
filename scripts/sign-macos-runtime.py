#!/usr/bin/env python3
"""Ad-hoc sign Mach-O runtime tools while preserving valid upstream signatures."""

import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path


def run(args):
    return subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)


def is_macho(path):
    result = run(["/usr/bin/file", "-L", "-b", str(path)])
    if result.returncode:
        raise RuntimeError("file failed for {}: {}".format(path, result.stderr.strip()))
    return "Mach-O" in result.stdout


def signature_entitlements(path):
    result = run(["/usr/bin/codesign", "-d", "--entitlements", ":-", str(path)])
    output = (result.stdout + "\n" + result.stderr).encode("utf-8", errors="replace")
    start = output.find(b"<?xml")
    end = output.rfind(b"</plist>")
    if start < 0 or end < start:
        return b"", False
    end += len(b"</plist>")
    details = run(["/usr/bin/codesign", "-d", "-vv", str(path)])
    detail_text = details.stdout + "\n" + details.stderr
    flags = re.search(r"flags=([^\r\n]+)", detail_text)
    hardened = bool(flags and "runtime" in flags.group(1))
    return output[start:end] + b"\n", hardened


def sign(path):
    verify = run(["/usr/bin/codesign", "--verify", "--strict", str(path)])
    if verify.returncode == 0:
        print("Preserving upstream signature:", path)
        return

    entitlements, hardened = signature_entitlements(path)
    command = ["/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none"]
    if hardened:
        command.extend(["--options", "runtime"])
    temp_path = None
    if entitlements:
        with tempfile.NamedTemporaryFile(prefix="skillmanager-entitlements-", suffix=".plist", delete=False) as temp:
            temp.write(entitlements)
            temp_path = temp.name
        command.extend(["--entitlements", temp_path])
    command.append(str(path))
    try:
        result = run(command)
        if result.returncode:
            raise RuntimeError(
                "codesign failed for {}: {}".format(path, result.stderr.strip())
            )
    finally:
        if temp_path and os.path.exists(temp_path):
            os.unlink(temp_path)

    verify = run(["/usr/bin/codesign", "--verify", "--strict", str(path)])
    if verify.returncode:
        raise RuntimeError(
            "codesign verification failed for {}: {}".format(path, verify.stderr.strip())
        )
    if entitlements:
        print("Signed with preserved entitlements:", path)
    else:
        print("Ad-hoc signed:", path)


def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: sign-macos-runtime.py <Contents/Resources/runtime>")
    root = Path(sys.argv[1]).resolve()
    if not root.is_dir():
        raise SystemExit("runtime directory not found: {}".format(root))
    seen = set()
    for candidate in sorted(root.rglob("*")):
        if not candidate.is_file() or not is_macho(candidate):
            continue
        resolved = candidate.resolve()
        identity = str(resolved)
        if identity in seen:
            continue
        seen.add(identity)
        sign(resolved)


if __name__ == "__main__":
    main()
