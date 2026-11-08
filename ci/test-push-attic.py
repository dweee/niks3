#!/usr/bin/env python3
"""Credential-free behavior checks for the Attic publishing helper."""
import importlib.util
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest
from unittest.mock import patch

HELPER = Path(__file__).with_name('push-attic.py')
spec = importlib.util.spec_from_file_location('push_attic', HELPER)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class AtticPublicationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.outputs = self.root / 'outputs.txt'
        self.paths = ['/nix/store/' + 'a' * 32 + '-package', '/nix/store/' + 'b' * 32 + '-another-output']
        self.outputs.write_text('\n'.join(self.paths + [self.paths[0]]) + '\n')
        self.binary = self.root / 'attic'
        self.binary.write_text('#!/bin/sh\nexit 0\n')
        self.binary.chmod(0o700)
        self.secret = 'synthetic-test-token-with-"quote"-and-\\slash'
        self.env = {'ATTIC_ENDPOINT': 'https://attic.monke.fail', 'ATTIC_CACHE': 'flakey',
                    'ATTIC_TOKEN': self.secret, 'ATTIC_BIN': str(self.binary),
                    'RUNNER_TEMP': str(self.root), 'GITHUB_STEP_SUMMARY': str(self.root / 'summary')}
        self.checked_dirs = []

    def inspect_invocation(self, args, *, input, text, env, check, stdout, stderr, timeout):
        expected = [str(self.binary), 'push', '--jobs', '1']
        if not self.checked_dirs:
            expected += ['--no-closure', '--ignore-upstream-cache-filter']
        expected += ['--stdin', 'ci:flakey']
        self.assertEqual(args, expected)
        self.assertEqual(input.splitlines(), self.paths)
        self.assertTrue(text)
        self.assertFalse(check)
        self.assertEqual(stdout, subprocess.PIPE)
        self.assertEqual(stderr, subprocess.STDOUT)
        self.assertGreater(timeout, 0)
        self.assertLessEqual(timeout, module.PUBLICATION_SECONDS)
        self.assertNotIn('ATTIC_TOKEN', env)
        self.assertNotIn(self.secret, json.dumps(args) + input + json.dumps(env))
        temporary = Path(env['XDG_CONFIG_HOME'])
        config = temporary / 'attic/config.toml'
        self.assertEqual(temporary.stat().st_mode & 0o777, 0o700)
        self.assertEqual(config.stat().st_mode & 0o777, 0o600)
        data = tomllib.loads(config.read_text())
        self.assertEqual(data['servers']['ci']['token'], self.secret)
        self.assertEqual(data['servers']['ci']['endpoint'], self.env['ATTIC_ENDPOINT'] + '/')
        self.checked_dirs.append(temporary)
        return subprocess.CompletedProcess(args, 0, stdout='')

    def test_exact_outputs_closures_permissions_and_cleanup(self):
        with patch.dict(os.environ, self.env, clear=True), patch.object(module.subprocess, 'run', side_effect=self.inspect_invocation):
            module.publish(self.outputs)
        self.assertEqual(len(self.checked_dirs), 2)
        self.assertEqual(self.checked_dirs[0], self.checked_dirs[1])
        self.assertTrue(all(not p.exists() for p in self.checked_dirs))
        self.assertIn('Uploaded 2 output(s) and runtime closures', (self.root / 'summary').read_text())
        self.assertNotIn(self.secret, (self.root / 'summary').read_text())

    def test_failed_root_upload_blocks_closure_upload_and_cleans_credentials(self):
        def failure(*args, **kwargs):
            self.inspect_invocation(*args, **kwargs)
            raise subprocess.CalledProcessError(17, args[0])
        with patch.dict(os.environ, self.env, clear=True), patch.object(module.subprocess, 'run', side_effect=failure) as call:
            with self.assertRaises(subprocess.CalledProcessError):
                module.publish(self.outputs)
            call.assert_called_once()
        self.assertEqual(len(self.checked_dirs), 1)
        self.assertTrue(all(not p.exists() for p in self.checked_dirs))
        self.assertFalse((self.root / 'summary').exists())

    def test_failed_dependency_upload_keeps_publication_red_and_cleans_credentials(self):
        def upload(*args, **kwargs):
            result = self.inspect_invocation(*args, **kwargs)
            if len(self.checked_dirs) == 2:
                raise subprocess.CalledProcessError(19, args[0])
            return result
        with patch.dict(os.environ, self.env, clear=True), patch.object(module.subprocess, 'run', side_effect=upload) as call:
            with self.assertRaises(subprocess.CalledProcessError):
                module.publish(self.outputs)
            self.assertEqual(call.call_count, 2)
        self.assertTrue(all(not p.exists() for p in self.checked_dirs))
        self.assertFalse((self.root / 'summary').exists())

    def test_transient_root_failure_retries_identical_roots_then_publishes_closure(self):
        calls = []
        def upload(args, **kwargs):
            self.assertNotIn('ATTIC_TOKEN', kwargs['env'])
            calls.append(args)
            return subprocess.CompletedProcess(args, int(len(calls) == 1),
                stdout='InternalServerError\n' if len(calls) == 1 else '')
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(module.subprocess, 'run', side_effect=upload), \
                patch.object(module.time, 'sleep') as sleep:
            module.publish(self.outputs)
        self.assertEqual(calls[0], calls[1])
        self.assertIn('--no-closure', calls[1])
        self.assertNotIn('--no-closure', calls[2])
        sleep.assert_called_once_with(15)
        self.assertIn('Uploaded 2 output(s)', (self.root / 'summary').read_text())

    def test_permanent_error_does_not_retry_or_publish_closure(self):
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(module.subprocess, 'run', return_value=
                    subprocess.CompletedProcess([], 1, stdout='HTTP 403 Forbidden\n')) as call, \
                patch.object(module.time, 'sleep') as sleep:
            with self.assertRaises(subprocess.CalledProcessError):
                module.publish(self.outputs)
        call.assert_called_once()
        sleep.assert_not_called()
        self.assertFalse((self.root / 'summary').exists())

    def test_transient_errors_are_bounded_and_remove_credentials(self):
        directories = []
        def upload(args, **kwargs):
            directories.append(Path(kwargs['env']['XDG_CONFIG_HOME']))
            return subprocess.CompletedProcess(args, 1, stdout='InternalServerError\n')
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(module.subprocess, 'run', side_effect=upload) as call, \
                patch.object(module.time, 'sleep') as sleep:
            with self.assertRaises(subprocess.CalledProcessError):
                module.publish(self.outputs)
        self.assertEqual(call.call_count, 5)
        self.assertEqual(sleep.call_count, 4)
        self.assertTrue(all(not p.exists() for p in directories))
        self.assertFalse((self.root / 'summary').exists())

    def test_one_deadline_covers_uploads_and_backoff(self):
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(module.time, 'monotonic', side_effect=[0, 0, module.PUBLICATION_SECONDS - 1]), \
                patch.object(module.subprocess, 'run', return_value=
                    subprocess.CompletedProcess([], 1, stdout='InternalServerError\n')), \
                patch.object(module.time, 'sleep') as sleep:
            with self.assertRaises(subprocess.TimeoutExpired):
                module.publish(self.outputs)
        sleep.assert_not_called()
        self.assertFalse((self.root / 'summary').exists())

    def test_client_diagnostic_redacts_the_publication_secret(self):
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(module.subprocess, 'run', return_value=
                    subprocess.CompletedProcess([], 1, stdout=self.secret)), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            with self.assertRaises(subprocess.CalledProcessError):
                module.publish(self.outputs)
        self.assertNotIn(self.secret, output.getvalue())
        self.assertIn('[redacted]', output.getvalue())

    def test_bad_configuration_never_contacts_server(self):
        cases = [('ATTIC_ENDPOINT', 'http://cache.example.test'),
                 ('ATTIC_ENDPOINT', 'https://person:password@cache.example.test'),
                 ('ATTIC_ENDPOINT', 'https://cache.example.test/?token=fixture'),
                 ('ATTIC_ENDPOINT', 'https://cache.example.test/#fragment'),
                 ('ATTIC_ENDPOINT', 'https://cache.example.test:99999'),
                 ('ATTIC_ENDPOINT', 'https://cache.example.test'), ('ATTIC_CACHE', 'other'), ('ATTIC_ENDPOINT', ''), ('ATTIC_CACHE', ''), ('ATTIC_CACHE', 'other:cache'),
                 ('ATTIC_CACHE', '../cache'), ('ATTIC_TOKEN', ''), ('ATTIC_BIN', '/missing/attic')]
        for name, value in cases:
            with self.subTest(name=name, value=value), patch.dict(os.environ, self.env | {name: value}, clear=True), patch.object(module.subprocess, 'run') as call:
                with self.assertRaises(ValueError): module.publish(self.outputs)
                call.assert_not_called()

    def test_empty_or_invalid_manifest_never_contacts_server(self):
        for content in ['', '\n', '/tmp/arbitrary\n', self.paths[0] + '/child\n', self.paths[0] + '.drv\n', self.paths[0] + '\nnot-a-store-path\n']:
            self.outputs.write_text(content)
            with self.subTest(content=content), patch.dict(os.environ, self.env, clear=True), patch.object(module.subprocess, 'run') as call:
                with self.assertRaises(ValueError): module.publish(self.outputs)
                call.assert_not_called()

    def test_invalid_settings_fail_cli_without_printing_secret(self):
        env = os.environ | self.env | {'ATTIC_CACHE': 'invalid:name'}
        result = subprocess.run(['python3', str(HELPER), '--outputs', str(self.outputs)],
                                env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertNotIn(self.secret, result.stdout + result.stderr)
        self.assertIn('configuration or output manifest is invalid', result.stderr)


if __name__ == '__main__':
    unittest.main()
