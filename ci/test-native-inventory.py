#!/usr/bin/env python3
"""Fail closed on inventory drift and preserve native checks and output exports."""
import copy
import importlib.util
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('native_matrix', ROOT / 'ci/matrix.py')
matrix = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(matrix)


class NativeInventory(unittest.TestCase):
    def setUp(self):
        self.inventory = json.loads((ROOT / 'nix/platforms.json').read_text())
        self.declared = {'packages': {}, 'checks': {}, 'devShells': {}}
        for system, names in self.inventory.items():
            self.declared['packages'][system] = names + ['default']
            self.declared['devShells'][system] = ['default']
            self.declared['checks'][system] = ['package-' + n for n in names + ['default']]
            self.declared['checks'][system] += ['devShell-default', 'treefmt', 'version-sync', 'go-unit-tests', 'golangci-lint']
            if system.endswith('-linux'):
                self.declared['checks'][system] += ['nixos-test-niks3', 'nixos-test-niks3-lix', 'nixos-test-read-proxy', 'nixos-test-external-signer', 'nixos-test-k3s']

    def test_every_unique_package_and_check_is_native_and_only_aliases_share_jobs(self):
        jobs = matrix.jobs(self.inventory, self.declared, 'darwin')['include'] + matrix.jobs(self.inventory, self.declared, 'others')['include']
        expected = {('packages', s, n) for s, names in self.inventory.items() for n in names}
        expected |= {('checks', s, n) for s, names in self.declared['checks'].items() for n in names if not n.startswith('package-')}
        actual = {(j['set'], j['system'], j['package']) for j in jobs}
        self.assertEqual(actual, expected)
        self.assertEqual(len(jobs), len(expected))
        self.assertEqual(len(jobs), 55)
        self.assertTrue(all(j['runner'] == matrix.RUNNERS[j['system']] for j in jobs))
        self.assertTrue(all(j['kvm'] == (j['system'] == 'x86_64-linux' and j['package'].startswith('nixos-test-')) for j in jobs))

    def test_channel_metadata_accounts_for_the_same_native_roots(self):
        policy = json.loads((ROOT / 'ci/channels.json').read_text())
        declared = {target['attribute'] for targets in policy['targets'].values() for target in targets}
        jobs = matrix.jobs(self.inventory, self.declared, 'darwin')['include'] + matrix.jobs(self.inventory, self.declared, 'others')['include']
        self.assertEqual(declared, {'.'.join((j['set'], j['system'], j['package'])) for j in jobs})

    def test_unclassified_system_package_and_missing_shell_alias_fail(self):
        for change in ['package', 'system', 'alias', 'shell']:
            declared = copy.deepcopy(self.declared)
            if change == 'package': declared['packages']['x86_64-linux'].append('new-export')
            if change == 'system': declared['checks']['x86_64-darwin'] = ['test']
            if change == 'alias': declared['checks']['aarch64-linux'].remove('package-niks3')
            if change == 'shell': declared['devShells']['aarch64-linux'].append('new-shell')
            with self.subTest(change=change), self.assertRaises(ValueError):
                matrix.jobs(self.inventory, declared, 'others')

    def test_new_check_is_included_and_manual_selection_stays_native(self):
        self.declared['checks']['aarch64-linux'].append('new-test')
        jobs = matrix.jobs(self.inventory, self.declared, 'others', 'new-test')['include']
        self.assertEqual([(j['set'], j['system'], j['package']) for j in jobs], [('checks', 'aarch64-linux', 'new-test')])
        for name in ['unknown', 'package-niks3']:
            with self.assertRaises(ValueError): matrix.jobs(self.inventory, self.declared, 'others', name)

    def test_workflows_export_all_outputs_and_keep_check_artifacts_distinct(self):
        for family in ['darwin', 'others']:
            workflow = (ROOT / f'.github/workflows/{family}-actions.yml').read_text()
            build = workflow.split('  build:', 1)[1].split('  publish:', 1)[0]
            self.assertIn('target=".#$TARGET_SET.$TARGET_SYSTEM.$TARGET_PACKAGE"', build)
            self.assertIn('-- nix build "$target^*"', build)
            self.assertIn("if: ${{ matrix.set == 'packages' }}", build)
            self.assertIn('kvm: ${{ matrix.kvm }}', build)
            self.assertEqual(workflow.count('name: nix-${{ matrix.system }}-${{ matrix.set }}-${{ matrix.package }}-${{ github.sha }}'), 2)
            self.assertIn("github.event_name != 'pull_request'", workflow)
            self.assertIn('--manifest cache-outputs.txt', build)


if __name__ == '__main__':
    unittest.main()
