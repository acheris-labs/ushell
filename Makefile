BINDIR ?= $(HOME)/.local/bin

.PHONY: build test install seed ios-acknowledgements test-ios-core test-ios-client ios-project ios-build test-ios-ui test-ios-app testflight

# Build the ush client for this machine.
build:
	go build -o bin/ush ./cmd/ush

# Unit tests. The e2e and menu suites need a rendezvous; see README.
test:
	go test ./...

bin/ushd: $(shell find cmd/ushd internal -name '*.go')
	go build -o bin/ushd ./cmd/ushd

# The menu suite against a local ushd, no container involved.
test-menu-local: build bin/ushd
	test/with-ushd.sh uv run --no-project python -m unittest test/menu_test.py

# The iOS app's core logic (key derivation, payload, list), on the Mac.
test-ios-core:
	swift test --package-path ios/USHKit --filter USHCoreTests

# The iOS client against a local ushd with three shared sessions.
test-ios-client: build bin/ushd
	test/with-ushd.sh ios/test/shared.sh swift test --package-path ios/USHKit --filter USHClientTests

IOS_SIM ?= iPad Pro 13-inch (M5)

# The Xcode project is generated from ios/project.yml (brew install xcodegen).
ios-project:
	cd ios && xcodegen generate --quiet

# Settings > ush > Acknowledgements: the license (and NOTICE) of every package
# the app links. Rerun when a package is added or bumped, and commit the result.
ios-acknowledgements: ios-project
	xcodebuild -resolvePackageDependencies -project ios/USH.xcodeproj \
	  -clonedSourcePackagesDirPath ios/build/SourcePackages -quiet
	uv run --no-project python ios/acknowledgements.py \
	  ios/build/SourcePackages/checkouts ios/App/Settings.bundle/Acknowledgements.plist

ios-build: ios-project
	xcodebuild build -project ios/USH.xcodeproj -scheme USH \
	  -destination 'platform=iOS Simulator,name=$(IOS_SIM)' -derivedDataPath ios/build \
	  -skipPackagePluginValidation -quiet

# The app's own unit tests (tabs, polling), on the simulator; no ushd needed.
test-ios-app: ios-project
	xcodebuild test -project ios/USH.xcodeproj -scheme USH -only-testing:USHTests \
	  -destination 'platform=iOS Simulator,name=$(IOS_SIM)' -derivedDataPath ios/build \
	  -skipPackagePluginValidation -quiet

# The app on the simulator against a local ushd: tabs, shortcuts, Ctrl-].
test-ios-ui: build bin/ushd ios-project
	test/with-ushd.sh ios/test/shared.sh sh -c 'TEST_RUNNER_USH_TEST_PORT=$$USH_TEST_PORT \
	  TEST_RUNNER_USH_TEST_SEED=$$USH_TEST_SEED \
	  TEST_RUNNER_USH_TEST_MOUSE=$$USH_TEST_MOUSE xcodebuild test -project ios/USH.xcodeproj \
	  -scheme USH -destination "platform=iOS Simulator,name=$(IOS_SIM)" -derivedDataPath ios/build \
	  -skipPackagePluginValidation -quiet'

# Ship an iOS build to TestFlight: archive, then upload to App Store Connect.
# Credentials come from ~/.appstoreconnect/config.mk (shared across all app
# projects, kept out of git). The app record (bundle id net.acheris.ush) must
# already exist in App Store Connect; Apple's API cannot create one.
-include $(HOME)/.appstoreconnect/config.mk
ASC_KEY_PATH ?= $(HOME)/.appstoreconnect/private_keys/AuthKey_$(ASC_KEY_ID).p8
IOS_BUILD    := $(shell date +%Y%m%d%H%M)

testflight: ios-project
	@test -n "$(ASC_KEY_ID)"    || { echo "set ASC_KEY_ID (App Store Connect API key id)";   exit 1; }
	@test -n "$(ASC_ISSUER_ID)" || { echo "set ASC_ISSUER_ID (App Store Connect issuer id)"; exit 1; }
	@test -f "$(ASC_KEY_PATH)"  || { echo "no API key .p8 at $(ASC_KEY_PATH)";                exit 1; }
	xcodebuild archive -project ios/USH.xcodeproj -scheme USH -configuration Release \
		-destination 'generic/platform=iOS' -archivePath ios/build/USH.xcarchive \
		-skipPackagePluginValidation -allowProvisioningUpdates \
		-authenticationKeyPath "$(ASC_KEY_PATH)" \
		-authenticationKeyID "$(ASC_KEY_ID)" \
		-authenticationKeyIssuerID "$(ASC_ISSUER_ID)" \
		CURRENT_PROJECT_VERSION=$(IOS_BUILD)
	xcodebuild -exportArchive -archivePath ios/build/USH.xcarchive \
		-exportOptionsPlist ios/ExportOptions.plist -exportPath ios/build/export \
		-allowProvisioningUpdates \
		-authenticationKeyPath "$(ASC_KEY_PATH)" \
		-authenticationKeyID "$(ASC_KEY_ID)" \
		-authenticationKeyIssuerID "$(ASC_ISSUER_ID)"
	@echo "Uploaded ush build $(IOS_BUILD). It'll appear in TestFlight once processing finishes."

# Test, build, then install to $(BINDIR). Nothing is installed if a test fails.
install: test build
	install -d $(BINDIR)
	install -m 755 bin/ush $(BINDIR)/ush

# Create .env with a new USH_SEED for the rendezvous. Never overwrites it.
seed: .env
.env:
	@umask 077 && printf 'USH_SEED=%s\n' "$$(go run ./cmd/ush seed)" > $@.tmp && mv $@.tmp $@
	@echo "wrote .env. The seed IS the key: keep it secret; use it with ush init --seed."
