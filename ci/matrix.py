#!/usr/bin/env python3
"""Account for every package, check and development shell on native builders."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
RUNNERS = {'aarch64-darwin': 'macos-15', 'aarch64-linux': 'ubuntu-24.04-arm', 'x86_64-linux': 'ubuntu-24.04'}
NAME = re.compile(r'[A-Za-z][A-Za-z0-9_-]*')


def jobs(inventory, declared, family, selected='all'):
    if set(inventory) != set(RUNNERS):
        raise ValueError('Unclassified native system')
    if set(declared) != {'packages', 'checks', 'devShells'}:
        raise ValueError('Missing advertised output set')
    for systems in declared.values():
        if set(systems) != set(RUNNERS):
            raise ValueError('Unclassified advertised native system')
        for names in systems.values():
            if len(names) != len(set(names)) or any(not NAME.fullmatch(n) for n in names):
                raise ValueError('Invalid advertised target name')
    result = []
    known = set()
    for system, names in inventory.items():
        if len(names) != len(set(names)) or set(declared['packages'][system]) != set(names) | {'default'}:
            raise ValueError('Native package inventory drift')
        # These checks are exact aliases in nix/checks/default.nix, not omitted
        # verification. Their selected package/shell job builds every output.
        expected_aliases = {'package-' + n for n in declared['packages'][system]}
        expected_shells = {'devShell-' + n for n in declared['devShells'][system]}
        if not expected_aliases | expected_shells <= set(declared['checks'][system]):
            raise ValueError('Missing package or development-shell check alias')
        for output_set, attributes in [('packages', names), ('checks', declared['checks'][system])]:
            for name in attributes:
                known.add(name)
                # Package check aliases share the package job; default aliases niks3.
                if output_set == 'checks' and name in expected_aliases:
                    continue
                if system.endswith('-darwin') == (family == 'darwin') and selected in ('all', name):
                    result.append({'system': system, 'runner': RUNNERS[system], 'package': name,
                                   'set': output_set, 'kvm': system == 'x86_64-linux' and name.startswith('nixos-test-')})
    if selected != 'all' and (selected not in known or selected.startswith('package-')):
        raise ValueError('Unknown or aliased target; select its package name')
    if len(result) > 256:
        raise ValueError('Native matrix exceeds job limit')
    return {'include': result}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('family', choices=['darwin', 'others'])
    parser.add_argument('--package', default='all')
    args = parser.parse_args()
    inventory = json.loads((ROOT / 'nix/platforms.json').read_text())
    expression = '''let f=builtins.getFlake %s; in {
      packages=builtins.mapAttrs (system: packages:
        assert packages.default.drvPath == packages.niks3.drvPath;
        builtins.attrNames packages) f.packages;
      checks=builtins.mapAttrs (system: checks:
        assert builtins.all (name: checks.${"package-"+name}.drvPath == f.packages.${system}.${name}.drvPath) (builtins.attrNames f.packages.${system});
        assert builtins.all (name: checks.${"devShell-"+name}.drvPath == f.devShells.${system}.${name}.drvPath) (builtins.attrNames f.devShells.${system});
        builtins.attrNames checks) f.checks;
      devShells=builtins.mapAttrs (_: builtins.attrNames) f.devShells;
    }''' % json.dumps('git+file://' + str(ROOT))
    raw = subprocess.check_output(['nix', 'eval', '--impure', '--json', '--option', 'accept-flake-config', 'false',
        '--option', 'otlp', 'false', '--option', 'allow-import-from-derivation', 'false', '--option', 'max-jobs', '0', '--option', 'builders', '',
        '--no-update-lock-file', '--no-write-lock-file', '--expr', expression], text=True, timeout=600)
    matrix = jobs(inventory, json.loads(raw), args.family, args.package)
    print(json.dumps(matrix, separators=(',', ':')))
    if os.environ.get('GITHUB_OUTPUT'):
        with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
            output.write('has_jobs=' + str(bool(matrix['include'])).lower() + '\n')


if __name__ == '__main__':
    main()
