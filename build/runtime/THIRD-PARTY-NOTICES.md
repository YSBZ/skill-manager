# Bundled runtime notices

These notices apply to the desktop packages staged by `scripts/prepare-runtime.py`.
The exact upstream license files are retained beside the runtime in `licenses/`,
and each Git distribution's own license directory is preserved during extraction.

## npm JavaScript package

- The official Node.js 26.13.0 archives are verified build inputs used to extract
  the npm package; the desktop runtime does not redistribute the Node executable,
  headers, or Node documentation. The upstream Node license text is retained as
  provenance in `licenses/Node-LICENSE`.
- npm 11.20.0: Artistic-2.0. Preserve `npm/LICENSE` and the dependency license
  texts copied into `licenses/npm-node-modules/`.

## Git toolchain

- Dugite Native release `v2.56.0-2` / Git 2.56.0: GPL-2.0-only. Preserve the full
  GPL text and the license files shipped in the runtime archive.
- Windows uses the Git for Windows MinGit 2.56.0.4 distribution inside Dugite Native.
  Preserve `git/LICENSE.txt`, `git/usr/share/licenses`,
  `git/ucrt64/share/licenses`, and the other bundled notice files without pruning.
- Git LFS 3.8.1 and Git Credential Manager 2.9.1: MIT. The Dugite Native runtime
  archive includes their executable files; copies of their upstream license texts
  are retained in this directory.

## Skills CLI

- `skills` 1.7.4: MIT.
- The published CLI artifact bundles additional Apache-2.0, ISC, and MIT components.
  Keep its `dist/THIRD-PARTY-LICENSES.md` and `ThirdPartyNoticeText.txt` files.
- `tar` and `yaml` are separately locked production dependencies. Their package
  license files remain in `skills/node_modules/`.

## WebView2 bootstrapper

Wails 2.12.0 embeds Microsoft's Evergreen WebView2 bootstrapper in the Windows
executable when built with `wv2runtime.embed`. It is an installer for the runtime,
not the runtime itself. Follow Microsoft's WebView2 distribution terms and keep
the Wails/WebView2 dependency notices with the distribution.

## Redistribution

`SOURCE-CODE.md` records the pinned source repositories and build path for the
copyleft components. A source URL by itself is not a substitute for providing the
complete corresponding source or an approved written offer. Review the complete
MinGit/MSYS2 component source set before redistributing the Windows bundle outside
the organization.
