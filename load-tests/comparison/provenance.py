#!/usr/bin/env python3
"""Bind prepared images to source snapshots before a comparison series."""
import sys
sys.dont_write_bytecode = True
import hashlib
import json
from pathlib import Path
import subprocess
import time

IMAGES = ["bank-compare-micro:local", "bank-compare-mono:local", "postgres:16-alpine", "redis:7.4-alpine"]

def sources(roots):
    result = {}
    for tag, root in zip(["micro","mono"],roots):
        root = Path(root).resolve()
        paths = subprocess.check_output(["git","-C",str(root),"ls-files","--cached","--others","--exclude-standard","-z"])
        files = {}
        for raw in sorted(set(paths.split(b"\0"))-{b""}):
            relative = raw.decode("utf-8")
            path = root / relative
            if path.is_file():
                files[relative] = hashlib.sha256(path.read_bytes()).hexdigest()
            elif path.is_symlink():
                files[relative] = "symlink:" + str(path.readlink())
            else:
                files[relative] = "missing"
        fingerprint = hashlib.sha256(json.dumps(files,sort_keys=True).encode()).hexdigest()
        result[tag] = dict(root=str(root),fingerprint=fingerprint,files=files,
            commit=subprocess.check_output(["git","-C",str(root),"rev-parse","HEAD"],text=True).strip())
    return result

def images():
    return {tag:subprocess.check_output(["docker","image","inspect","--format","{{.Id}}",tag],text=True).strip()
        for tag in IMAGES}

if __name__=="__main__":
    mode, micro, mono, directory = sys.argv[1:]
    dest = Path(directory)
    current = sources([micro,mono])
    if mode=="capture":
        dest.mkdir(parents=True,exist_ok=True)
        (dest/"source-before.json").write_text(json.dumps(current,indent=2))
    elif mode=="seal":
        before=json.loads((dest/"source-before.json").read_text())
        if before!=current:
            raise SystemExit("Sources changed during prepare. Run prepare again after finishing edits.")
        (dest/"manifest.json").write_text(json.dumps(dict(schema=1,created_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime()),
            sources=current,images=images()),indent=2))
        print("Prepared source/image manifest:",dest/"manifest.json")
    elif mode=="verify":
        try:
            manifest=json.loads((dest/"manifest.json").read_text())
        except (OSError,ValueError):
            raise SystemExit("No prepared manifest. Run course-suite.sh prepare.")
        if manifest.get("sources")!=current:
            raise SystemExit("Sources changed after prepare. Run course-suite.sh prepare again.")
        if manifest.get("images")!=images():
            raise SystemExit("Prepared image IDs changed. Run course-suite.sh prepare again.")
        print("Prepared sources and image IDs match.")
    else:
        raise SystemExit("Usage: provenance.py capture|seal|verify MICRO_DIR MONO_DIR PREPARED_DIR")
