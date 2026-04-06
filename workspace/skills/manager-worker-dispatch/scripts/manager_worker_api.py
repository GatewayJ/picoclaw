#!/usr/bin/env python
"""Simple CLI for worker management and task dispatch in CSGClaw."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
from typing import Any
from urllib import error, request


DEFAULT_BASE_URL = "http://127.0.0.1:18080"
CSGCLAW_BASE_URL_ENV = "CSGCLAW_BASE_URL"
CSGCLAW_ACCESS_TOKEN_ENV = "CSGCLAW_ACCESS_TOKEN"
MANAGER_API_BASE_URL_ENV = "MANAGER_API_BASE_URL"
MANAGER_API_TOKEN_ENV = "MANAGER_API_TOKEN"
MANAGER_API_TIMEOUT_ENV = "MANAGER_API_TIMEOUT"


def build_url(base_url: str, path: str) -> str:
    return f"{base_url.rstrip('/')}/{path.lstrip('/')}"


class CSGClawAPI:
    def __init__(self, base_url: str, token: str | None, timeout: float, dry_run: bool) -> None:
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.timeout = timeout
        self.dry_run = dry_run

    def _headers(self) -> dict[str, str]:
        headers = {"Content-Type": "application/json", "Accept": "application/json"}
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        return headers

    def request_json(self, method: str, path: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
        url = build_url(self.base_url, path)
        body = None if payload is None else json.dumps(payload).encode("utf-8")

        if self.dry_run:
            return self._mock_response(method, path, url, payload)

        req = request.Request(url, data=body, method=method, headers=self._headers())
        try:
            with request.urlopen(req, timeout=self.timeout) as resp:
                raw = resp.read().decode("utf-8")
        except error.HTTPError as exc:
            detail = exc.read().decode("utf-8", errors="replace")
            raise SystemExit(f"HTTP {exc.code} for {method} {url}: {detail}") from exc
        except error.URLError as exc:
            raise SystemExit(f"Request failed for {method} {url}: {exc.reason}") from exc

        if not raw.strip():
            return {}

        try:
            return json.loads(raw)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"Invalid JSON response from {method} {url}: {raw}") from exc

    def _mock_response(
        self,
        method: str,
        path: str,
        url: str,
        payload: dict[str, Any] | None,
    ) -> dict[str, Any] | list[dict[str, Any]]:
        if method == "GET" and path == "/api/v1/workers":
            return []

        result: dict[str, Any] = {
            "dry_run": True,
            "method": method,
            "url": url,
            "payload": payload,
        }

        if method == "POST" and path == "/api/v1/workers":
            worker_name = str((payload or {}).get("name") or "worker")
            worker_role = str((payload or {}).get("role") or "worker")
            worker_id = str((payload or {}).get("id") or f"u-{worker_name}")
            result.update(
                {
                    "id": worker_id,
                    "name": worker_name,
                    "role": "worker",
                    "description": (payload or {}).get("description", ""),
                    "status": "running",
                    "model_id": (payload or {}).get("model_id", ""),
                }
            )
            return result

        if method == "POST" and path == "/api/v1/im/agents/join":
            result["joined"] = True
            return result

        if method == "POST" and path.startswith("/api/bots/") and path.endswith("/messages/send"):
            result["message_id"] = "dry-run-message-id"
            return result

        return result

    def list_workers(self) -> list[dict[str, Any]]:
        data = self.request_json("GET", "/api/v1/workers")
        if isinstance(data, list):
            return [item for item in data if isinstance(item, dict)]
        raise SystemExit(f"Unexpected worker list response: {json.dumps(data, ensure_ascii=False)}")

    def create_worker(
        self,
        name: str,
        role: str,
        worker_id: str | None,
        description: str | None,
        model_id: str | None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {"name": name, "role": role}
        if worker_id:
            payload["id"] = worker_id
        if description:
            payload["description"] = description
        if model_id:
            payload["model_id"] = model_id
        return self.request_json("POST", "/api/v1/workers", payload)

    def join_agent_to_room(
        self,
        agent_id: str,
        room_id: str,
        inviter_id: str | None,
        locale: str | None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "agent_id": agent_id,
            "room_id": room_id,
        }
        if inviter_id:
            payload["inviter_id"] = inviter_id
        if locale:
            payload["locale"] = locale
        return self.request_json("POST", "/api/v1/im/agents/join", payload)

    def send_bot_message(self, bot_id: str, room_id: str, text: str) -> dict[str, Any]:
        return self.request_json(
            "POST",
            f"/api/bots/{bot_id}/messages/send",
            {"room_id": room_id, "text": text},
        )


def normalize_description(value: str) -> str:
    return " ".join(strings for strings in re.split(r"\s+", value.strip().lower()) if strings)


def find_worker_by_description(workers: list[dict[str, Any]], description: str) -> dict[str, Any] | None:
    target = normalize_description(description)
    if not target:
        return None

    for worker in workers:
        worker_description = normalize_description(str(worker.get("description") or ""))
        if worker_description == target:
            return worker

    for worker in workers:
        worker_description = normalize_description(str(worker.get("description") or ""))
        if worker_description and (target in worker_description or worker_description in target):
            return worker

    for worker in workers:
        worker_description = normalize_description(str(worker.get("description") or ""))
        if not worker_description:
            continue
        target_tokens = set(target.split())
        worker_tokens = set(worker_description.split())
        if target_tokens and target_tokens.issubset(worker_tokens):
            return worker
    return None


def get_worker_identity(worker: dict[str, Any]) -> tuple[str, str]:
    worker_id = str(worker.get("id") or "").strip()
    worker_name = str(worker.get("name") or "").strip()
    if not worker_id:
        raise SystemExit(f"Worker id missing: {json.dumps(worker, ensure_ascii=False)}")
    if not worker_name:
        raise SystemExit(f"Worker name missing: {json.dumps(worker, ensure_ascii=False)}")
    return worker_id, worker_name


def build_assignment_text(worker_name: str, task: str, mention: str | None) -> str:
    custom_mention = (mention or "").strip()
    if custom_mention:
        return f"{custom_mention} {task}".strip()
    return f"@{worker_name} {task}".strip()


def load_api(args: argparse.Namespace) -> CSGClawAPI:
    base_url = (
        args.base_url
        or os.environ.get(CSGCLAW_BASE_URL_ENV)
        or os.environ.get(MANAGER_API_BASE_URL_ENV)
        or DEFAULT_BASE_URL
    )
    token = (
        args.token
        or os.environ.get(CSGCLAW_ACCESS_TOKEN_ENV)
        or os.environ.get(MANAGER_API_TOKEN_ENV)
    )
    timeout = float(os.environ.get(MANAGER_API_TIMEOUT_ENV, "30"))
    return CSGClawAPI(base_url=base_url, token=token, timeout=timeout, dry_run=args.dry_run)


def add_common_args(parser: argparse.ArgumentParser) -> None:
    parser.add_argument(
        "--base-url",
        help=(
            "CSGClaw API base URL. "
            f"Falls back to {CSGCLAW_BASE_URL_ENV}, {MANAGER_API_BASE_URL_ENV}, default {DEFAULT_BASE_URL}."
        ),
    )
    parser.add_argument(
        "--token",
        help=f"Bearer token. Falls back to {CSGCLAW_ACCESS_TOKEN_ENV}, {MANAGER_API_TOKEN_ENV}.",
    )
    parser.add_argument("--dry-run", action="store_true", help="Print requests instead of sending them.")


def cmd_list_workers(args: argparse.Namespace) -> int:
    api = load_api(args)
    print(json.dumps(api.list_workers(), ensure_ascii=False, indent=2))
    return 0


def cmd_create_worker(args: argparse.Namespace) -> int:
    api = load_api(args)
    worker = api.create_worker(args.name, args.role, args.id, args.description, args.model_id)
    print(json.dumps(worker, ensure_ascii=False, indent=2))
    return 0


def cmd_join_worker(args: argparse.Namespace) -> int:
    api = load_api(args)
    result = api.join_agent_to_room(args.worker_id, args.room_id, args.inviter_id, args.locale)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


def cmd_send_message(args: argparse.Namespace) -> int:
    api = load_api(args)
    result = api.send_bot_message(args.bot_id, args.room_id, args.text)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


def cmd_ensure_and_dispatch(args: argparse.Namespace) -> int:
    api = load_api(args)
    worker = find_worker_by_description(api.list_workers(), args.description)

    if worker is None:
        worker = api.create_worker(args.name, args.role, args.id, args.description, args.model_id)

    worker_id, worker_name = get_worker_identity(worker)
    text = build_assignment_text(worker_name, args.task, args.mention)

    result = {
        "worker": worker,
        "join": api.join_agent_to_room(worker_id, args.room_id, args.inviter_id, args.locale),
        "message": api.send_bot_message(args.bot_id, args.room_id, text),
        "text": text,
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Call CSGClaw worker and bot APIs.")
    add_common_args(parser)

    subparsers = parser.add_subparsers(dest="command", required=True)

    list_workers = subparsers.add_parser("list-workers", help="Fetch all workers.")
    add_common_args(list_workers)
    list_workers.set_defaults(func=cmd_list_workers)

    create_worker = subparsers.add_parser("create-worker", help="Create a worker.")
    add_common_args(create_worker)
    create_worker.add_argument("--name", required=True, help="Worker name.")
    create_worker.add_argument("--role", default="worker", help="Worker role. Default: worker.")
    create_worker.add_argument("--id", help="Optional worker id.")
    create_worker.add_argument("--description", help="Optional worker description.")
    create_worker.add_argument("--model-id", help="Optional model id.")
    create_worker.set_defaults(func=cmd_create_worker)

    join_worker = subparsers.add_parser("join-worker", help="Join a worker to a room.")
    add_common_args(join_worker)
    join_worker.add_argument("--room-id", required=True, help="Room id.")
    join_worker.add_argument("--worker-id", required=True, help="Worker agent id.")
    join_worker.add_argument("--inviter-id", default="u-manager", help="Inviter id. Default: u-manager.")
    join_worker.add_argument("--locale", help="Optional locale, for example zh-CN.")
    join_worker.set_defaults(func=cmd_join_worker)

    send_message = subparsers.add_parser("send-message", help="Send a bot message to a room.")
    add_common_args(send_message)
    send_message.add_argument("--bot-id", default="u-manager", help="Bot id used as message sender.")
    send_message.add_argument("--room-id", required=True, help="Room id.")
    send_message.add_argument("--text", required=True, help="Message text.")
    send_message.set_defaults(func=cmd_send_message)

    ensure_dispatch = subparsers.add_parser(
        "ensure-and-dispatch",
        help="Find or create a worker by description, join the room, then send a mention message by bot.",
    )
    add_common_args(ensure_dispatch)
    ensure_dispatch.add_argument("--room-id", required=True, help="Room id.")
    ensure_dispatch.add_argument("--bot-id", default="u-manager", help="Bot id used as message sender.")
    ensure_dispatch.add_argument("--role", default="worker", help="Worker role when creating. Default: worker.")
    ensure_dispatch.add_argument("--name", required=True, help="Worker name if creation is needed.")
    ensure_dispatch.add_argument("--task", required=True, help="Task description.")
    ensure_dispatch.add_argument("--mention", help="Optional custom mention prefix, for example '@bob 请处理'.")
    ensure_dispatch.add_argument("--id", help="Optional worker id when creating.")
    ensure_dispatch.add_argument("--description", required=True, help="Worker capability description used for matching and creation.")
    ensure_dispatch.add_argument("--model-id", help="Optional model id when creating.")
    ensure_dispatch.add_argument("--inviter-id", default="u-manager", help="Inviter id. Default: u-manager.")
    ensure_dispatch.add_argument("--locale", help="Optional locale, for example zh-CN.")
    ensure_dispatch.set_defaults(func=cmd_ensure_and_dispatch)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
