#!/usr/bin/env python3
"""Select native jobs from the package's explicit platform inventory."""
import argparse
import json
import os
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("family", choices=["darwin", "others"])
parser.add_argument("--package", default="all")
args = parser.parse_args()
inventory = json.loads((Path(__file__).resolve().parents[1] / "nix/platforms.json").read_text())
runners = {"aarch64-darwin": "macos-15", "aarch64-linux": "ubuntu-24.04-arm", "x86_64-linux": "ubuntu-24.04"}
if args.package != "all" and not any(args.package in names for names in inventory.values()):
    raise ValueError("Unknown package")
jobs = [{"system": system, "runner": runners[system], "package": package}
        for system, names in inventory.items()
        if system.endswith("-darwin") == (args.family == "darwin")
        for package in names if args.package in ["all", package]]
print(json.dumps({"include": jobs}, separators=(",", ":")))
if os.environ.get("GITHUB_OUTPUT"):
    with open(os.environ["GITHUB_OUTPUT"], "a") as out:
        out.write("has_jobs=" + str(bool(jobs)).lower() + "\n")
