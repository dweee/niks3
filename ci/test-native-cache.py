#!/usr/bin/env python3
"""The optional consumer build selector must never accept a moving or injected ref."""
import contextlib
import io
from pathlib import Path
import runpy
import unittest
from unittest.mock import patch


class SelectorTests(unittest.TestCase):
    def select(self, revision):
        with patch.dict('os.environ', {'BUILD_NIXPKGS_REVISION': revision}, clear=True), \
             patch('subprocess.run') as run, contextlib.redirect_stdout(io.StringIO()):
            runpy.run_path(str(Path(__file__).with_name('select-build-nixpkgs.py')))
            return run

    def test_default_preserves_owning_lock(self):
        self.select('').assert_not_called()

    def test_moving_or_injected_refs_are_rejected(self):
        for revision in ['main', 'a'*39, 'a'*41, 'a'*40+'\n', 'github:private/repo', '$(id)']:
            with self.subTest(revision=revision), self.assertRaises(SystemExit):
                self.select(revision)

    def test_exact_commit_uses_only_public_nixpkgs(self):
        revision = 'a'*40
        self.select(revision).assert_called_once_with(
            ['nix', 'flake', 'lock', '--override-input', 'nixpkgs',
             'github:NixOS/nixpkgs/'+revision], check=True)


if __name__ == '__main__':
    unittest.main()
