#!/usr/bin/env python3
"""Check deployment settings for both sides of an Abby-to-Yak upgrade."""
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent


def check(settings, expected):
    # Use fixture values, never the caller's deployment credentials or env file.
    env = {k: v for k, v in os.environ.items() if not k.startswith(("YAK_", "ABBY_"))}
    env.update(settings)
    result = subprocess.run(
        ["docker", "compose", "-p", "yak-compose-check", "--env-file", os.devnull,
         "-f", str(ROOT / "deploy/compose.yaml"), "config", "--format", "json"],
        env=env, capture_output=True, text=True,
    )
    if result.returncode:
        sys.stderr.write(result.stderr)
        result.check_returncode()
    services = json.loads(result.stdout)["services"]
    database = f"postgres://abby:{expected['DB_PASSWORD']}@postgres:5432/abby?sslmode=disable"
    for name in ("migrate", "yak"):
        service = services[name]
        values = service["environment"]
        assert values["YAK_DATABASE_URL"] == database, name
        assert values["ABBY_DATABASE_URL"] == database, name
        assert service["image"] == expected["IMAGE"], name
    values = services["yak"]["environment"]
    assert values["YAK_ROLE"] == values["ABBY_ROLE"] == expected["ROLE"]
    assert values["YAK_LISTEN"] == values["ABBY_LISTEN"] == ":8080"
    assert services["postgres"]["environment"]["POSTGRES_PASSWORD"] == expected["DB_PASSWORD"]
    assert services["yak"]["ports"][0]["published"] == expected["PORT"]


old = {"DB_PASSWORD": "fixture-old", "IMAGE": "abby:previous", "ROLE": "api", "PORT": "19321"}
new = {"DB_PASSWORD": "fixture-new", "IMAGE": "yak:candidate", "ROLE": "worker", "PORT": "19322"}
legacy = {"ABBY_" + k: v for k, v in old.items()}
current = {"YAK_" + k: v for k, v in new.items()}
for name, settings, expected in (
    ("legacy settings", legacy, old),
    ("current settings", current, new),
    ("current settings take precedence", {**legacy, **current}, new),
):
    check(settings, expected)
    print(f"PASS: {name}; equivalent environment for old and new images")
