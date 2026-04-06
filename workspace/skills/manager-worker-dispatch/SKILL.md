---
name: manager-worker-dispatch
description: Use this skill to manage and dispatch tasks to workers. Triggers include: any request to break a task into subtasks and assign them to workers or bots; listing, creating, or selecting workers via the manager_worker_api; sending @-mention messages to workers inside a room; coordinating sequential or parallel multi-worker execution pipelines; dispatching frontend, backend, or QA work to the right worker by capability. Do NOT use for generic project planning, single-agent tasks.
---

# Manager Worker Dispatch

Interpret the admin request, break it into capability-aligned subtasks, and dispatch each subtask through the real CSGClaw API.

Use the bundled script for deterministic API calls instead of rewriting request code inline.

## Workflow

1. Read the admin request and split it into concrete deliverables.
2. Infer the required capability for each deliverable.
3. List existing workers with `scripts/manager_worker_api.py list-workers`.
4. Reuse an existing worker when its `description` matches the needed capability and scope.
5. Create a worker when no existing worker description clearly matches.
6. Add the worker to the target room when needed.
7. Dispatch the subtask by having a bot send a message in that room, and make sure every manager-to-worker message starts with an `@` prefix, for example `@bob 你来写前端代码`.
8. If the request is sequential, wait for the previous worker to finish before dispatching the next worker with another `@` message.
9. If the request is parallel, dispatch multiple workers at the same time with separate `@` messages.

Keep assignments specific. Include the expected output, scope, and capability.

## Capability Mapping

Map work to worker descriptions before calling the API. Do not select a worker by its `role` field. Read each worker's `description` and choose the one whose described responsibility best matches the task.

- Frontend UI, page work, styling, interaction: `frontend`
- APIs, services, storage, data flow: `backend`
- Validation, regression checks, acceptance checks: `qa`
- Cross-cutting coordination or unclear requests: split the task first, then assign

If the admin request implies several capabilities, create one assignment per capability instead of sending one broad task to a single worker.

## Script Usage

Use `scripts/manager_worker_api.py` for API operations.

Common commands:

```bash
cd ~/.picoclaw/workspace/skills/manager-worker-dispatch
python scripts/manager_worker_api.py list-workers
python scripts/manager_worker_api.py create-worker --name alex --description "qa regression testing"
python scripts/manager_worker_api.py join-worker --room-id room-123 --worker-id u-alex
python scripts/manager_worker_api.py send-message --room-id room-123 --text "@alex 你来进行测试，验证登录流程并记录回归风险"
python scripts/manager_worker_api.py ensure-and-dispatch --room-id room-123 --name bob --description "frontend ui styling interaction" --task "你来写前端代码，实现设置页 UI" --dry-run
```

Read `references/api-contract.md` before changing endpoint names, payload fields, or environment variables.

When the script runs inside a CSGClaw box, it reads these environment variables automatically:

- `CSGCLAW_BASE_URL`
- `CSGCLAW_ACCESS_TOKEN`

## Operating Rules

- Prefer an existing worker whose `description` matches the task before creating a new one.
- Keep worker names short and stable.
- Add the worker to the room before dispatching the task when the room does not already include that worker.
- When the manager assigns work, always prefix the worker mention with `@`, and tell the worker to reply with `@` as well.
- For sequential work, do not dispatch the next worker until the previous worker has completed.
- For parallel work, multiple workers may be dispatched together with separate `@` messages.
- Use `--dry-run` first when endpoint compatibility is uncertain.
- If the API response shape differs from the documented assumptions, patch the script instead of improvising ad hoc requests.
