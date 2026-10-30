BINARY := skillmanager
DIST    := dist
GUIDE   := docs/dist-readme.md
# Single source of truth for the user-facing version and monotonic release code:
# the macOS bundle's CFBundleShortVersionString and CFBundleVersion. Packaging runs
# on macOS, so PlistBuddy is available. Bump them in build/macos/Info.plist and every
# artifact filename (zips + dmg) follows automatically. Defined BEFORE the flag vars
# below so their -X values expand to the real values (:= is immediate).
VERSION := $(shell /usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" build/macos/Info.plist 2>/dev/null)
RELEASE_CODE := $(shell /usr/libexec/PlistBuddy -c "Print :CFBundleVersion" build/macos/Info.plist 2>/dev/null)
# Public builds use the GitHub release feed. A local ignored build/update.local file
# may override it for a private build; keep any tokens out of the source tree.
UPDATE_FEED ?= https://raw.githubusercontent.com/YSBZ/skill-manager/main/release/latest.json
-include build/update.local
UPDATE_LDFLAGS := -X main.updateFeed=$(UPDATE_FEED) -X main.updateToken=$(UPDATE_TOKEN) -X main.releaseCode=$(RELEASE_CODE)
FEEDBACK_LDFLAGS := -X main.feedbackEndpoint=$(FEEDBACK_ENDPOINT) -X main.feedbackToken=$(FEEDBACK_TOKEN)
# Optional shared content service origin. Keep it empty for local-only/public builds;
# SKILLMANAGER_CONTENT_URL can still override the baked default at runtime.
CONTENT_SERVICE_URL ?=
CONTENT_LDFLAGS := -X skillmanage/internal/server.ContentServiceURL=$(CONTENT_SERVICE_URL)
PRIVATE_LDFLAGS := $(UPDATE_LDFLAGS) $(FEEDBACK_LDFLAGS) $(CONTENT_LDFLAGS)
# main.version is stamped into the binary so `skillmanager` prints its release on startup.
GOFLAGS := -trimpath -ldflags "-s -w -X main.version=$(VERSION) $(PRIVATE_LDFLAGS)"
# Windows: -H=windowsgui builds a windowless binary so a double-click runs the
# daemon in the background (it auto-opens the browser) instead of leaving a
# console window — and a quick exit no longer flashes a console.
WINFLAGS := -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$(VERSION) $(PRIVATE_LDFLAGS)"

.PHONY: build test vet fmt build-all build-contentd package desktop-app desktop-win winres install-desktop desktop-dist desktop-dmg clean clean-dist runtime-cli runtime-darwin-arm64 runtime-darwin-amd64 runtime-windows-amd64 runtime-macos runtime-windows runtime-all

# go-winres lives in GOPATH/bin; winres bootstraps it if missing.
GOWINRES := $(shell go env GOPATH)/bin/go-winres

# macOS desktop app (Wails): a native window wrapping the daemon. Needs CGO +
# the UniformTypeIdentifiers framework (which `wails build` would add for us);
# we build both arches and lipo them into a universal SkillManager.app.
DESKTOP_APP     := SkillManager.app
DESKTOP_LDFLAGS := -s -w -X main.version=$(VERSION) $(PRIVATE_LDFLAGS) -extldflags '-framework UniformTypeIdentifiers'
PYTHON          ?= python3
RUNTIME_ROOT    := $(DIST)/system-node-runtime
BUNDLE_RUNTIME  ?= 1

ifneq ($(filter $(BUNDLE_RUNTIME),0 1),$(BUNDLE_RUNTIME))
$(error BUNDLE_RUNTIME must be 0 or 1)
endif

ifneq ($(BUNDLE_RUNTIME),0)
DESKTOP_MAC_RUNTIME_DEP := runtime-macos
DESKTOP_WIN_RUNTIME_DEP := runtime-windows
else
DESKTOP_MAC_RUNTIME_DEP :=
DESKTOP_WIN_RUNTIME_DEP :=
endif

# `npm ci` is local to this build helper, ignores lifecycle scripts and never
# installs globally. The staging script downloads only lock-listed official assets.
runtime-cli:
	npm ci --prefix build/runtime/cli --omit=dev --ignore-scripts --registry=https://registry.npmjs.org

runtime-darwin-arm64: runtime-cli
	$(PYTHON) scripts/prepare-runtime.py --platform darwin-arm64 --output "$(RUNTIME_ROOT)"

runtime-darwin-amd64: runtime-cli
	$(PYTHON) scripts/prepare-runtime.py --platform darwin-amd64 --output "$(RUNTIME_ROOT)"

runtime-windows-amd64: runtime-cli
	$(PYTHON) scripts/prepare-runtime.py --platform windows-amd64 --output "$(RUNTIME_ROOT)"

runtime-macos: runtime-darwin-arm64 runtime-darwin-amd64

runtime-windows: runtime-windows-amd64

runtime-all: runtime-macos runtime-windows

desktop-app: $(DESKTOP_MAC_RUNTIME_DEP)
	@mkdir -p $(DIST)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -trimpath -tags "desktop,production" -ldflags "$(DESKTOP_LDFLAGS)" -o $(DIST)/smd-arm64 ./desktop
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -trimpath -tags "desktop,production" -ldflags "$(DESKTOP_LDFLAGS)" -o $(DIST)/smd-amd64 ./desktop
	@lipo -create -output $(DIST)/smd-universal $(DIST)/smd-arm64 $(DIST)/smd-amd64
	@rm -f $(DIST)/smd-arm64 $(DIST)/smd-amd64
	@rm -rf "$(DIST)/$(DESKTOP_APP)"
	@mkdir -p "$(DIST)/$(DESKTOP_APP)/Contents/MacOS" "$(DIST)/$(DESKTOP_APP)/Contents/Resources"
	@cp build/macos/Info.plist "$(DIST)/$(DESKTOP_APP)/Contents/Info.plist"
	@cp build/macos/AppIcon.icns "$(DIST)/$(DESKTOP_APP)/Contents/Resources/AppIcon.icns"
	@mv $(DIST)/smd-universal "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanager-desktop"
	@chmod +x "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanager-desktop"
	@set -eu; \
	if [ "$(BUNDLE_RUNTIME)" = "1" ]; then \
	  mkdir -p "$(DIST)/$(DESKTOP_APP)/Contents/Resources/runtime"; \
	  cp -R "$(RUNTIME_ROOT)/darwin-arm64" "$(DIST)/$(DESKTOP_APP)/Contents/Resources/runtime/"; \
	  cp -R "$(RUNTIME_ROOT)/darwin-amd64" "$(DIST)/$(DESKTOP_APP)/Contents/Resources/runtime/"; \
	  $(PYTHON) scripts/sign-macos-runtime.py "$(DIST)/$(DESKTOP_APP)/Contents/Resources/runtime"; \
	fi
	codesign --force --sign - --timestamp=none "$(DIST)/$(DESKTOP_APP)/Contents/MacOS/skillmanager-desktop"
	codesign --force --sign - --timestamp=none "$(DIST)/$(DESKTOP_APP)"
	codesign --verify --deep --strict --verbose=2 "$(DIST)/$(DESKTOP_APP)"
	@echo "built $(DIST)/$(DESKTOP_APP)"

# Regenerate the Windows resource object (desktop/rsrc_windows_amd64.syso): the
# app icon (same artwork as macOS — converted from build/macos/AppIcon.icns via
# sips), version info, and a GUI manifest. The "_windows_amd64" suffix means
# `go build` links it ONLY for windows/amd64; darwin/linux builds ignore it, so
# the committed .syso never affects the macOS app. Run after a version bump.
# macOS-only (uses sips), consistent with the rest of packaging.
winres:
	@test -x "$(GOWINRES)" || go install github.com/tc-hib/go-winres@latest
	@sips -s format png -z 1024 1024 build/macos/AppIcon.icns --out build/windows/AppIcon.png >/dev/null
	@cd desktop && "$(GOWINRES)" simply --arch amd64 --manifest gui \
	  --icon ../build/windows/AppIcon.png \
	  --product-name "SkillManager" --file-description "SkillManager" \
	  --product-version "$(VERSION)" --file-version "$(VERSION).0"
	@echo "regenerated desktop/rsrc_windows_amd64.syso (icon + v$(VERSION))"

# Windows desktop app (Wails native window). Wails uses pure-Go WebView2 bindings
# on Windows, so this needs NO CGO and NO Windows machine — it cross-compiles from
# any host (macOS/Linux). -H=windowsgui = no console window on double-click.
# Depends on winres so the .exe always carries the icon + current version.
# Output: a shareable zip with the .exe + a Windows usage guide.
desktop-win: winres $(DESKTOP_WIN_RUNTIME_DEP)
	@mkdir -p "$(DIST)/pkg"
	@rm -f "$(DIST)/pkg"/SkillManager-desktop-windows-*.zip "$(DIST)/pkg"/SkillManager-windows-desktop-*.zip   # 清掉新旧命名，避免并存
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -tags "desktop,production,wv2runtime.embed" -ldflags "-s -w -H=windowsgui -X main.version=$(VERSION) $(PRIVATE_LDFLAGS)" -o "$(DIST)/SkillManager.exe" ./desktop
	@set -eu; \
	d="$(DIST)/pkg/SkillManager-desktop-windows-v$(VERSION)"; rm -rf "$$d"; mkdir -p "$$d"; \
	cp "$(DIST)/SkillManager.exe" "$$d/SkillManager.exe"; cp build/windows/dist-readme.md "$$d/使用说明.md"; \
	if [ "$(BUNDLE_RUNTIME)" = "1" ]; then mkdir -p "$$d/runtime"; cp -R "$(RUNTIME_ROOT)/windows-amd64" "$$d/runtime/"; fi; \
	$(PYTHON) -c 'import shutil,sys;shutil.make_archive(sys.argv[1], "zip", root_dir=sys.argv[2], base_dir=sys.argv[3])' "$(DIST)/pkg/SkillManager-desktop-windows-v$(VERSION)" "$(DIST)/pkg" "SkillManager-desktop-windows-v$(VERSION)"; \
	rm -rf "$$d" "$(DIST)/SkillManager.exe"; echo "built $(DIST)/pkg/SkillManager-desktop-windows-v$(VERSION).zip"; \
	ls -lh "$(DIST)/pkg/SkillManager-desktop-windows-v$(VERSION).zip"

# Drag-to-install disk image. Installing to /Applications via the dmg avoids App
# Translocation (which a quarantined zip-from-Downloads triggers) — worth trying
# if the app misbehaves when launched from a download. Still ad-hoc: first open
# needs right-click → Open (only notarization removes that).
desktop-dmg: desktop-app
	@rm -rf "$(DIST)/dmg" $(DIST)/SkillManager-v*.dmg   # 清掉旧版本 dmg，避免新旧并存
	@mkdir -p "$(DIST)/dmg"
	@cp -R "$(DIST)/$(DESKTOP_APP)" "$(DIST)/dmg/"
	@cp build/macos/dist-readme.md "$(DIST)/dmg/安装说明.md"
	@ln -s /Applications "$(DIST)/dmg/Applications"
	@hdiutil create -volname "SkillManager $(VERSION)" -srcfolder "$(DIST)/dmg" -ov -format ULMO "$(DIST)/SkillManager-v$(VERSION).dmg" >/dev/null
	@rm -rf "$(DIST)/dmg"
	@echo "built $(DIST)/SkillManager-v$(VERSION).dmg"

# Shareable zip of the desktop app + recipient guide (Gatekeeper instructions).
desktop-dist: desktop-app
	@mkdir -p "$(DIST)/pkg"
	@rm -f $(DIST)/pkg/SkillManager-desktop-macos-*.zip $(DIST)/pkg/SkillManager-macos-*.zip   # 清掉新旧命名，避免并存
	@set -eu; \
	d="$(DIST)/pkg/SkillManager-desktop-macos"; rm -rf "$$d"; mkdir -p "$$d"; \
	cp -R "$(DIST)/$(DESKTOP_APP)" "$$d/"; cp build/macos/dist-readme.md "$$d/README.md"; \
	(cd "$(DIST)/pkg" && rm -f "SkillManager-desktop-macos-v$(VERSION).zip" && ditto -c -k --keepParent SkillManager-desktop-macos "SkillManager-desktop-macos-v$(VERSION).zip"); \
	rm -rf "$$d"; ls -lh "$(DIST)/pkg/SkillManager-desktop-macos-v$(VERSION).zip"

# Install the desktop app: /Applications if writable, else ~/Applications.
# Removes any old SkillManage.app (pre-rename) so the two don't coexist.
install-desktop: desktop-app
	@set -e; \
	if [ -w /Applications ]; then dest=/Applications; else dest="$$HOME/Applications"; mkdir -p "$$dest"; fi; \
	rm -rf "$$dest/SkillManage.app"; \
	rm -rf "$$dest/$(DESKTOP_APP)"; cp -R "$(DIST)/$(DESKTOP_APP)" "$$dest/"; \
	echo "installed $$dest/$(DESKTOP_APP)"

# Host build.
build:
	go build $(GOFLAGS) -o $(BINARY) .

build-contentd:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/contentd-linux-amd64 ./cmd/contentd

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# Cross-compiled single binaries (UI embedded, no separate frontend build).
# WSL uses the linux build. We do NOT wipe all of dist here (that would clobber
# sibling artifacts like the dmg / windows-desktop zip); we only overwrite our
# own intermediates. The host ./skillmanager (repo root) is never touched.
build-all:
	@mkdir -p $(DIST)
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -o $(DIST)/skillmanager-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/skillmanager-darwin-amd64 .
	GOOS=windows GOARCH=amd64 go build $(WINFLAGS) -o $(DIST)/skillmanager-windows-amd64.exe .
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -o $(DIST)/skillmanager-linux-amd64 .

# Shareable per-platform zips: each bundles the single binary (named uniformly
# as skillmanager / skillmanager.exe) plus the recipient guide. Send one zip; the
# recipient needs no Go toolchain. Clears prior web zips (all versions) first so
# a version bump never leaves new + old side by side.
package: build-all
	@mkdir -p "$(DIST)/pkg"
	@rm -f $(DIST)/pkg/SkillManager-web-*.zip $(DIST)/pkg/skillmanager-*.zip   # 清掉新旧命名网页版 zip，避免并存
	@set -e; \
	for spec in \
	  "darwin-arm64:skillmanager:macos-apple-silicon" \
	  "darwin-amd64:skillmanager:macos-intel" \
	  "linux-amd64:skillmanager:linux-wsl" \
	  "windows-amd64.exe:skillmanager.exe:windows"; do \
	  src=$${spec%%:*}; rest=$${spec#*:}; name=$${rest%%:*}; label=$${rest#*:}; \
	  d="$(DIST)/pkg/SkillManager-web-$$label-v$(VERSION)"; mkdir -p "$$d"; \
	  cp "$(DIST)/skillmanager-$$src" "$$d/$$name"; cp "$(GUIDE)" "$$d/README.md"; \
	  (cd "$(DIST)/pkg" && zip -q -r "SkillManager-web-$$label-v$(VERSION).zip" "SkillManager-web-$$label-v$(VERSION)"); \
	  rm -rf "$$d"; \
	done; \
	rm -f $(DIST)/skillmanager-*; \
	ls -lh $(DIST)/pkg/*.zip

clean-dist:
	rm -rf $(DIST)

clean:
	rm -rf $(DIST) $(BINARY)
