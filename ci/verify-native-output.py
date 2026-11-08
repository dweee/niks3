#!/usr/bin/env python3
"""Check exported package contents without starting services or signer sessions."""
from pathlib import Path
import os
import subprocess
import sys
import tarfile


def run(arguments):
    result = subprocess.run(arguments, capture_output=True, timeout=60, check=True)
    assert result.stdout or result.stderr, 'Expected executable output'


def verify(name, root):
    commands = {'niks3', 'niks3-hook', 'niks3-server', 'mock-oidc-server',
                'niks3-openbao-signer', 'niks3-pkcs11-signer'}
    if name in commands:
        run([str(root / 'bin' / name), '--help'])
    elif name == 'niks3-tests':
        for binary in ('client', 'server', 'server-oidc', 'hook', 'server-signing', 'openbao-signer'):
            executable = root / 'bin' / ('niks3-' + binary + '.test')
            assert executable.is_file() and os.access(executable, os.X_OK)
            # server's TestMain starts PostgreSQL and RustFS even for -test.list.
            if binary != 'server':
                run([str(executable), '-test.list=.'])
    elif name == 'benchmark-closure':
        run([str(root / 'bin/python3'), '-c',
             'import requests, flask, django, numpy, pandas, scipy, PIL, cryptography, yaml; print("Imports passed")'])
    elif name == 'benchmark-disk-image':
        with root.open('rb') as image:
            image.seek(1080)
            assert image.read(2) == b'\x53\xef', 'Missing ext4 superblock'
    elif name == 'niks3-docker':
        with tarfile.open(root, 'r:*') as archive:
            assert any(Path(member.name).name in ('manifest.json', 'index.json') for member in archive), 'Missing image manifest'
    else:
        raise ValueError('Unknown native package')


if __name__ == '__main__':
    verify(sys.argv[1], Path(sys.argv[2]))
