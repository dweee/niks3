#!/usr/bin/env python3
"""Optionally reproduce a consumer's exact public nixpkgs without private inputs."""
import os
import re
import subprocess

revision = os.environ.get('BUILD_NIXPKGS_REVISION', '')
if revision:
    if not re.fullmatch('[0-9a-f]{40}', revision):
        raise SystemExit('Expected an immutable public nixpkgs commit')
    subprocess.run(['nix', 'flake', 'lock', '--override-input', 'nixpkgs',
                    'github:NixOS/nixpkgs/' + revision], check=True)
    print('Selected public nixpkgs revision ' + revision)
