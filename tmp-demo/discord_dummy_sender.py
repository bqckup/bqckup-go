#!/usr/bin/env python3
import json
import requests

payload = {
    "username": "Bqckup",
    "embeds": [
        {
            "title": "Daily Backup Report · 2026-09-01",
            "description": "Backup activity was recorded, but 1 run(s) failed.",
            "color": 2467079,
            "fields": [
                {"name": "Server IP", "value": "srv-backup (192.168.1.10)", "inline": True},
                {"name": "Storage", "value": "local-main, s3-primary", "inline": True},
                {"name": "Total Runs", "value": "3 (OK: 2 | Failed: 1)", "inline": True},
                {"name": "Total Size", "value": "3.1 GiB", "inline": True},
                {"name": "demo-prod", "value": "Runs: 2 | OK: 1 | Failed: 1 | Last: failed"},
                {"name": "demo-staging", "value": "Runs: 1 | OK: 1 | Failed: 0 | Last: success"}
            ],
            "footer": {"text": "Bqckup monitoring • 2026-09-01 08:00"}
        }
    ]
}

resp = requests.post(
    "http://127.0.0.1:8788",
    data=json.dumps(payload),
    headers={"Content-Type": "application/json"},
    timeout=10,
)
print("status:", resp.status_code)
print(resp.text)
