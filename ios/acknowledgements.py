"""Writes the app's Settings > ush > Acknowledgements page: the license of
every package compiled into the app, and its NOTICE where it has one, which
Apache-2.0 asks to be passed on too.

    make ios-acknowledgements

reads the packages Xcode resolved into ios/build/SourcePackages and rewrites
App/Settings.bundle/Acknowledgements.plist. The output is committed, so a
build never depends on this; run it again when a package is added or bumped.
"""

import os
import plistlib
import sys

# What the app links, by checkout directory. swift-argument-parser, swift-asn1
# and swift-system are resolved as well, but nothing the app links uses them:
# a build's USH.LinkFileList lists every module that goes in.
PACKAGES = [
    "SwiftTerm",
    "swift-atomics",
    "swift-collections",
    "swift-crypto",
    "swift-nio",
    "swift-nio-ssh",
]

LICENSE_NAMES = ["LICENSE", "LICENSE.txt", "LICENSE.md"]
NOTICE_NAMES = ["NOTICE", "NOTICE.txt", "NOTICE.md"]


def read_first(directory, names):
    """The text of the first of names in directory, or None."""
    for name in names:
        path = os.path.join(directory, name)
        if os.path.isfile(path):
            with open(path, encoding="utf-8") as f:
                return f.read().strip()
    return None


def entries(checkouts, packages=PACKAGES):
    """(package, text) for each package: its license, then its NOTICE."""
    out = []
    for package in packages:
        directory = os.path.join(checkouts, package)
        license_text = read_first(directory, LICENSE_NAMES)
        if license_text is None:
            raise SystemExit(f"{package}: no license file in {directory}")
        notice = read_first(directory, NOTICE_NAMES)
        out.append((package, license_text if notice is None else f"{license_text}\n\n{notice}"))
    return out


def settings_page(items):
    """A Settings.bundle child pane: one group per package, its text below."""
    return {
        "Title": "Acknowledgements",
        "StringsTable": "Acknowledgements",
        "PreferenceSpecifiers": [
            {"Type": "PSGroupSpecifier", "FooterText": "ush is built with these open source packages."},
        ] + [
            {"Type": "PSGroupSpecifier", "Title": package, "FooterText": text}
            for package, text in items
        ],
    }


def main(argv):
    if len(argv) != 3:
        raise SystemExit("usage: acknowledgements.py CHECKOUTS_DIR OUTPUT_PLIST")
    checkouts, output = argv[1], argv[2]
    with open(output, "wb") as f:
        plistlib.dump(settings_page(entries(checkouts)), f, sort_keys=False)


if __name__ == "__main__":
    main(sys.argv)
