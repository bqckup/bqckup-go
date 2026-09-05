#!/usr/bin/env python3
"""Receive and print Bqckup generic webhook requests for local testing."""

import argparse
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class WebhookHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        print(f"\n--- webhook request {self.command} {self.path} ---", flush=True)
        print(f"Content-Type: {self.headers.get('Content-Type', '')}", flush=True)
        try:
            payload = json.loads(body)
            print(json.dumps(payload, indent=2, ensure_ascii=True), flush=True)
        except json.JSONDecodeError:
            print(body.decode("utf-8", errors="replace"), flush=True)
        self.send_response(204)
        self.end_headers()

    def do_GET(self):
        self.send_response(405)
        self.send_header("Allow", "POST")
        self.end_headers()

    def log_message(self, format_string, *args):
        return


def main():
    parser = argparse.ArgumentParser(description="Local generic webhook receiver")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8787)
    args = parser.parse_args()
    server = ThreadingHTTPServer((args.host, args.port), WebhookHandler)
    print(f"Listening for POST requests on http://{args.host}:{args.port}/webhook", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nStopping webhook receiver.")
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
