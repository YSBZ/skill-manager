# Corresponding source for bundled runtimes

The binary artifacts and source pins below are recorded in `build/runtime-lock.json`.
All release downloads are fetched over TLS and checked against the lock before
extraction. The desktop runtime contains the Git binaries and npm JavaScript package,
not a complete source bundle; it does not contain a Node.js executable.

## Node.js and npm

- Node.js 26.13.0 source: <https://github.com/nodejs/node/archive/refs/tags/v26.13.0.tar.gz>
- npm 11.20.0 source: <https://github.com/npm/cli/archive/refs/tags/v11.20.0.tar.gz>
- Official Node archive checksums: <https://nodejs.org/dist/v26.13.0/SHASUMS256.txt.asc>
- The lock pins official macOS arm64/x64 and Windows x64 Node archives as build
  inputs. Staging extracts the npm package from each archive; it omits the Node
  executable, headers, and Node distribution documentation from the final runtime.
- The signed Node.js checksum manifest was verified with the official
  <https://github.com/nodejs/release-keys> keyring before recording these hashes.

## Git toolchain

- Dugite Native packaging source, tag `v2.56.0-2`:
  <https://github.com/desktop/dugite-native/archive/refs/tags/v2.56.0-2.tar.gz>
- macOS Git source, submodule commit
  `a018953688f1b10bddf91bff8747068f5f4746a4`:
  <https://github.com/git/git/archive/a018953688f1b10bddf91bff8747068f5f4746a4.tar.gz>
- Windows Git for Windows source, tag `v2.56.0.windows.4`:
  <https://github.com/git-for-windows/git/archive/refs/tags/v2.56.0.windows.4.tar.gz>
- MinGit 2.56.0.4 x64 binary input and digest:
  <https://github.com/git-for-windows/git/releases/download/v2.56.0.windows.4/MinGit-2.56.0.4-64-bit.zip>
- Git LFS 3.8.1 source: <https://github.com/git-lfs/git-lfs/archive/refs/tags/v3.8.1.tar.gz>
- Git Credential Manager 2.9.1 source:
  <https://github.com/git-ecosystem/git-credential-manager/archive/refs/tags/v2.9.1.tar.gz>
- The Git archive hashes in the runtime lock match both the GitHub release API's
  SHA-256 digests and Dugite Native's published `.sha256` sidecars.

Dugite Native's `docs/overview.md` describes its build process. Its release
packaging scripts consume a Git source tree, build the macOS variant, or unpack
the pinned MinGit distribution for Windows; they then package platform files and
checksums. The Windows MinGit archive contains more than Git itself. Its sources
and notices are maintained in the Git for Windows `build-extra`, `MINGW-packages`,
and MSYS2 package sources. The bundled MinGit tree records package revisions in
`etc/package-versions.txt`, which is retained in the staged runtime. Those exact
package sources and their complete source set must be retained for a full
corresponding-source offer.

## Skills CLI and runtime dependencies

- Skills 1.7.4 source: <https://github.com/vercel-labs/skills/archive/refs/tags/v1.7.4.tar.gz>
- The npm tarball integrity and all production dependency versions are pinned in
  `build/runtime/cli/package-lock.json`. The shipped `node_modules` retain source
  and license text for the locked JavaScript packages.

## Source release requirement

This file provides provenance and acquisition instructions; it does not assert
that URLs alone satisfy any corresponding-source obligation. Before external
redistribution of the GPL Git/MinGit binaries, either distribute a complete,
matching source bundle (including the MinGit/MSYS2 build inputs and modifications)
or have an approved written offer covering every recipient. The binary-only
runtime staging target must not be treated as the source-compliant release target.
