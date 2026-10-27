"""The acknowledgements page generator.

Run with:  uv run --no-project python -m unittest ios/test/acknowledgements_test.py
"""

import os
import plistlib
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import acknowledgements  # noqa: E402


def package(root, name, files):
    os.makedirs(os.path.join(root, name))
    for file, text in files.items():
        with open(os.path.join(root, name, file), "w", encoding="utf-8") as f:
            f.write(text)


class AcknowledgementsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def test_notice_follows_the_license(self):
        package(self.root, "nio", {"LICENSE.txt": "Apache text\n", "NOTICE.txt": "NIO notice\n"})
        package(self.root, "term", {"LICENSE": "MIT text\n"})
        got = acknowledgements.entries(self.root, ["nio", "term"])
        self.assertEqual(got, [("nio", "Apache text\n\nNIO notice"), ("term", "MIT text")])

    def test_a_package_without_a_license_is_an_error(self):
        package(self.root, "bare", {"README.md": "hi"})
        with self.assertRaises(SystemExit):
            acknowledgements.entries(self.root, ["bare"])

    def test_page_is_a_settings_child_pane(self):
        page = acknowledgements.settings_page([("term", "MIT text")])
        out = os.path.join(self.root, "Acknowledgements.plist")
        with open(out, "wb") as f:
            plistlib.dump(page, f)
        with open(out, "rb") as f:
            back = plistlib.load(f)
        groups = back["PreferenceSpecifiers"]
        self.assertTrue(all(g["Type"] == "PSGroupSpecifier" for g in groups))
        self.assertEqual(groups[-1], {"Type": "PSGroupSpecifier", "Title": "term", "FooterText": "MIT text"})


if __name__ == "__main__":
    unittest.main()
