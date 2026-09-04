#!/usr/bin/env python3
"""Convert the legacy Bqckup config tree to schema-v2 YAML.

Usage:
  python3 scripts/convert_legacy_config.py --input-dir /etc/bqckup \
      --output-dir /etc/bqckup-v2

The input tree is never modified. Credential-bearing output files are written
with mode 0600 and credential values are never printed.
"""

from __future__ import annotations

import argparse
import configparser
import re
import shutil
import sys
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:
    print("error: PyYAML is required; install it with: python3 -m pip install PyYAML", file=sys.stderr)
    raise SystemExit(2)
im

INTERVALS = {
    "hourly": "1h",
    "daily": "24h",
    "weekly": "168h",
    "monthly": "720h",
}
SAFE_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")


def as_bool(value: Any, default: bool = False) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    return str(value).strip().lower() in {"1", "yes", "true", "on", "enabled"}


def as_list(value: Any) -> list[Any]:
    if value is None:
        return []
    return value if isinstance(value, list) else [value]


def interval(value: Any) -> str:
    raw = str(value or "daily").strip().lower()
    return INTERVALS.get(raw, raw)


def legacy_site(document: dict[str, Any], warnings: list[str]) -> dict[str, Any]:
    old = document.get("bqckup") or {}
    options = old.get("options") or {}
    name = str(old.get("name") or "").strip()
    if not name:
        raise ValueError("site is missing bqckup.name")
    if not SAFE_NAME.fullmatch(name):
        warnings.append(f"site {name!r} contains characters that may not be accepted by v2")

    incremental = old.get("incremental") or {}
    is_incremental = as_bool(incremental.get("enable"))
    site: dict[str, Any] = {
        "name": name,
        "enabled": as_bool(old.get("enabled"), True),
        "backup_mode": "incremental" if is_incremental else "full",
        "sources": {
            "files": {
                "include": [str(path) for path in as_list(old.get("path"))],
                "exclude": [str(path) for path in as_list(old.get("exclude_path"))],
                "follow_symlinks": as_bool(options.get("follow_symlink")),
            },
            "databases": [],
        },
        "destinations": [],
        "policy": {
            "minimum_interval": interval(options.get("interval")),
            "keep_last": int(str(options.get("retention") or "7").strip()),
        },
    }

    if is_incremental:
        site["incremental"] = {"password": str(incremental.get("password") or "")}
        if not site["incremental"]["password"]:
            warnings.append(f"site {name!r} is incremental but has no repository password")

    database = old.get("database")
    if isinstance(database, dict):
        engine = str(database.get("type") or "").lower()
        if engine == "postgresql":
            engine = "postgres"
        db = {
            "name": str(database.get("name") or name),
            "enabled": as_bool(database.get("enabled"), True),
            "engine": engine,
            "host": str(database.get("host") or "localhost"),
            "port": int(database.get("port") or (3306 if engine == "mysql" else 5432)),
            "database": str(database.get("name") or ""),
            "username": str(database.get("user") or ""),
            "password": str(database.get("password") or ""),
        }
        site["sources"]["databases"].append(db)
        if engine not in {"mysql", "postgres"}:
            warnings.append(f"site {name!r} uses unsupported database type {engine!r}")

    storage = options.get("storage")
    if storage:
        site["destinations"].append({"storage": str(storage)})
    else:
        warnings.append(f"site {name!r} has no legacy destination")
    if as_bool(old.get("save_locally")):
        warnings.append(f"site {name!r} used save_locally; v2 converter keeps only configured destinations")
    if old.get("notification_email"):
        warnings.append(f"site {name!r} notification_email was not converted; configure root notifications in v2")
    return {"site": site}


def convert(args: argparse.Namespace) -> int:
    source, target, relocate_legacy = resolve_paths(args)
    if relocate_legacy:
        legacy_backup = Path("/etc/bqckup_old")
        if legacy_backup.exists():
            print(f"error: cannot preserve legacy config; {legacy_backup} already exists", file=sys.stderr)
            return 2
        source.rename(legacy_backup)
        source = legacy_backup
        print(f"moved legacy config to {legacy_backup}")
    if not source.is_dir():
        print(f"error: input directory does not exist: {source}", file=sys.stderr)
        return 2
    if target.exists():
        if not args.force:
            print(f"error: output directory already exists: {target}; use --force to replace it", file=sys.stderr)
            return 2
        shutil.rmtree(target)

    storage_path = source / "config" / "storages.yml"
    if not storage_path.exists():
        storage_path = source / "config" / "storages.yaml"
    if not storage_path.exists():
        print(f"error: legacy storage file not found under {source / 'config'}", file=sys.stderr)
        return 2

    warnings: list[str] = []
    with storage_path.open(encoding="utf-8") as handle:
        storage_doc = yaml.safe_load(handle) or {}
    storages: dict[str, Any] = {}
    for name, old in (storage_doc.get("storages") or {}).items():
        old = old or {}
        provider = str(old.get("provider") or "s3").lower()
        storage_type = "r2" if provider == "r2" else "s3"
        if provider not in {"s3", "r2"}:
            warnings.append(f"storage {name!r} provider {provider!r} mapped to s3")
        item = {
            "type": storage_type,
            "bucket": str(old.get("bucket") or ""),
            "access_key_id": str(old.get("access_key_id") or ""),
            "secret_access_key": str(old.get("secret_access_key") or ""),
            "region": str(old.get("region") or ""),
            "endpoint": str(old.get("endpoint") or ""),
            "primary": as_bool(old.get("primary")),
        }
        storages[str(name)] = {key: value for key, value in item.items() if value != ""}

    root = {"server_id": "", "app": {
        "state_database": "/var/lib/bqckup/bqckup.db",
        "temporary_directory": "/var/lib/bqckup/tmp",
        "lock_directory": "/var/lib/bqckup/locks",
        "log_level": "info",
        "log_file": "/var/log/bqckup/bqckup.log",
    }}
    cnf_path = source / "bqckup.cnf"
    if cnf_path.exists():
        parser = configparser.ConfigParser()
        parser.read(cnf_path, encoding="utf-8")
        root["server_id"] = parser.get("bqckup", "root_folder_name", fallback="")
        webhook = parser.get("notification", "discord_webhook_url", fallback="").strip()
        if as_bool(parser.get("notification", "enabled", fallback="0")) and webhook:
            root["notifications"] = {"channels": {"discord": {"type": "discord", "webhook_url": webhook}}, "routes": [{"events": ["all"], "channels": ["discord"]}]}
        if as_bool(parser.get("notification", "monthly_report_enabled", fallback="0")):
            warnings.append("monthly_report_enabled is not supported by v2 and was not converted")

    site_docs: list[tuple[str, dict[str, Any]]] = []
    for path in sorted((source / "sites").glob("*.y*ml")):
        try:
            with path.open(encoding="utf-8") as handle:
                document = yaml.safe_load(handle) or {}
            if isinstance(document, dict) and isinstance(document.get("site"), dict) and "bqckup" not in document:
                warnings.append(f"skipping existing v2 site file {path}")
                continue
            converted = legacy_site(document, warnings)
        except (OSError, yaml.YAMLError, ValueError) as error:
            print(f"error: could not convert site file {path}: {error}", file=sys.stderr)
            return 2
        name = converted["site"]["name"]
        destination_names = {item["storage"] for item in converted["site"]["destinations"]}
        missing = sorted(destination_names - storages.keys())
        for destination in missing:
            warnings.append(f"site {name!r} references undefined storage {destination!r}; v2 validation will reject it")
        site_docs.append((name, converted))

    target.mkdir(mode=0o700, parents=True)
    (target / "config").mkdir(mode=0o700)
    (target / "sites").mkdir(mode=0o700)
    write_yaml(target / "bqckup.yaml", root, 0o600)
    write_yaml(target / "config" / "storages.yaml", {"storages": storages}, 0o600)
    for name, document in site_docs:
        write_yaml(target / "sites" / f"{name}.yaml", document, 0o600)

    print(f"converted {len(site_docs)} site(s) and {len(storages)} storage(s) to {target}")
    for warning in warnings:
        print(f"warning: {warning}", file=sys.stderr)
    return 0


def resolve_paths(args: argparse.Namespace) -> tuple[Path, Path, bool]:
    if args.input_dir:
        source = Path(args.input_dir).resolve()
    else:
        legacy_default = Path("/etc/bqckup_old")
        active_default = Path("/etc/bqckup")
        relocate_legacy = False
        if is_legacy_tree(legacy_default):
            source = legacy_default.resolve()
            default_target = active_default.resolve()
        elif is_legacy_tree(active_default):
            source = active_default.resolve()
            default_target = active_default.resolve()
            relocate_legacy = True
            print(
                "legacy config detected in /etc/bqckup; it will be moved to "
                "/etc/bqckup_old before conversion",
                file=sys.stderr,
            )
        else:
            print("error: no legacy config detected in /etc/bqckup_old or /etc/bqckup; pass --input-dir", file=sys.stderr)
            raise SystemExit(2)
        if args.output_dir:
            target = Path(args.output_dir).resolve()
        else:
            target = default_target
        return source, target, relocate_legacy
    target = Path(args.output_dir or "/etc/bqckup").resolve()
    if source == target:
        print("error: input and output directories must be different", file=sys.stderr)
        raise SystemExit(2)
    return source, target, False


def is_legacy_tree(directory: Path) -> bool:
    if not directory.is_dir():
        return False
    storage = directory / "config" / "storages.yml"
    if not storage.exists():
        storage = directory / "config" / "storages.yaml"
    if not storage.exists():
        return False
    for path in (directory / "sites").glob("*.y*ml"):
        try:
            with path.open(encoding="utf-8") as handle:
                document = yaml.safe_load(handle) or {}
        except (OSError, yaml.YAMLError):
            return False
        if isinstance(document, dict) and isinstance(document.get("bqckup"), dict):
            return True
    return False


def write_yaml(path: Path, value: dict[str, Any], mode: int) -> None:
    with path.open("w", encoding="utf-8") as handle:
        yaml.safe_dump(value, handle, sort_keys=False, allow_unicode=False, default_flow_style=False)
    path.chmod(mode)


def main() -> int:
    parser = argparse.ArgumentParser(description="Convert legacy Bqckup config files to schema-v2 YAML")
    parser.add_argument("--input-dir", help="legacy config root (auto-detects /etc/bqckup_old)")
    parser.add_argument("--output-dir", help="new config root (defaults to /etc/bqckup)")
    parser.add_argument("--force", action="store_true", help="replace an existing output directory")
    return convert(parser.parse_args())


if __name__ == "__main__":
    raise SystemExit(main())
