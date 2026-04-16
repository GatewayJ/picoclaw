---
name: manager-worker-dispatch
description: Use this skill to break an admin request into capability-aligned subtasks, provision or reuse workers through csgclaw-cli, write the dispatch plan to todo.json, and start sequential task tracking with manager_worker_api. Do NOT use for generic planning or single-agent execution.
---

# Manager Worker Dispatch

Break an admin request into clear tasks, choose workers by capability, and dispatch them through CSGClaw's real local interfaces in sequence.

Use `csgclaw-cli` for room, bot, member, and worker operations.
Use `scripts/manager_worker_api.py` only for `start-tracking` and `stop-tracking`.
Check command help for the current CLI surface instead of writing ad hoc API requests.

## Fast Path

If the admin explicitly asks the manager to arrange or reuse workers such as `ux`, `dev`, and `qa`, do this directly:

1. Do not do the implementation work yourself.
2. Do not use `message` for progress chatter or to restate the request.
3. Do not use `spawn` or `subagent`.
4. Run `csgclaw-cli bot list`, reuse matching available workers, and create a worker only if a required capability is missing or the matching worker is `unavailable`.
5. Ensure every chosen worker has joined the target room and verify this with `csgclaw-cli member list`.
6. Only after all required workers are present in the room, write `todo.json` under `~/.picoclaw/workspace/projects/<slug>/todo.json`.
7. Only after that room-membership check passes, start tracking with `scripts/manager_worker_api.py start-tracking`.
8. Send at most one concise final room reply after tracking starts successfully.

If you already know this workflow and the relevant command surface is clear, do not reread this file just to paraphrase it back to the user.
Do not inspect or modify project implementation files before dispatch unless you need to choose the project slug or update `todo.json`.

## Workflow

1. Break the admin request into concrete deliverables.
2. Match each task to the needed capability; run `csgclaw-cli bot list` first, reuse by matching `description`, and create a worker only when needed or when the matching worker is `unavailable`.
3. Ensure every required worker has joined the target room, then verify the full required worker set with `csgclaw-cli member list`.
4. Choose a suitable project directory under `~/.picoclaw/workspace/projects`; create a short slug directory if none fits.
5. Write or overwrite `todo.json` in that directory as the only source of truth for the current dispatch plan, but only after the room-membership verification succeeds.
6. Start `scripts/manager_worker_api.py start-tracking` against that `todo.json`, but only after all required workers are confirmed present in the room.
7. Let the tracker own sequential handoff; workers must reply in-room with results or blockers, and neither the manager nor workers should manually assign the next worker while tracking is active.

## Room Membership Gate

Room membership is a hard gate before dispatch.

- Determine the complete set of workers required by `todo.json` before starting tracking.
- Add any missing worker with `csgclaw-cli member create`.
- After adding workers, run `csgclaw-cli member list --room-id <target_room_id> --channel <current_channel>` and verify every required worker ID/name appears in the target room.
- Do not write the final `todo.json`, run `start-tracking`, or send any assignment/progress message until every required worker is confirmed present in that room.
- If any required worker is still missing from the room, fix membership first; never rely on `start-tracking` to invite or discover missing workers.

Inside a manager/worker box, the shared project tree is `~/.picoclaw/workspace/projects`.
On the host machine, that same mount is `~/.csgclaw/projects`.
When reporting a project path back to a human user, translate the in-box path to the host path. Example:

- in box: `~/.picoclaw/workspace/projects/kanban-board`
- on host: `~/.csgclaw/projects/kanban-board`

## todo.json

`todo.json` must be valid JSON.

- Single task: write one task object.
- Multiple tasks: write `{ "tasks": [...] }`; array order is dispatch order.

Each task should keep these fields:

- `id`: task number, required, use `1`, `2`, `3` in dispatch order
- `assignee`: owner, usually a worker name or role-like label
- `category`: short task type such as `feature`, `bug`, or `test`
- `description`: task summary
- `steps`: array of execution steps
- `passes`: completion state, usually `false` at the start
- `progress_note`: progress, result, or blocker note, usually an empty string at the start

`id` must always be present and should increase sequentially with the task order in `todo.json`.

While tracking is active, task completion is a two-part gate:

- update `passes` to `true` and write a useful `progress_note`
- post an in-room mention message to manager with blocker summary if blocked

Tool trace messages are not enough for handoff. The tracker waits for both the `todo.json` update and the assignee's room reply before dispatching the next task.

Example:

```json
{
  "tasks": [
    {
      "id": 1,
      "assignee": "frontend",
      "category": "feature",
      "description": "Build the settings page UI and connect the save action.",
      "steps": [
        "Implement the settings page layout",
        "Connect the save action to the API",
        "Reply to the manager with the implementation summary"
      ],
      "passes": false,
      "progress_note": ""
    },
    {
      "id": 2,
      "assignee": "qa",
      "category": "test",
      "description": "Validate the main settings page flows after frontend delivery.",
      "steps": [
        "Verify the main edit and save flows",
        "Record regressions and blockers",
        "Reply to the manager with QA results"
      ],
      "passes": false,
      "progress_note": ""
    }
  ]
}
```

## Capability Mapping

Choose workers by `description`, not just by `role`.

- `frontend`: UI, page work, styling, interaction
- `backend`: APIs, services, storage, data flow
- `qa`: validation, regression, acceptance checks

Split cross-capability work into multiple tasks instead of giving one vague package to a single worker.

## Command Usage

Create a room:

```bash
csgclaw-cli room create --title test-room --creator-id ou_xxx --channel <current_channel>
```

List workers:

```bash
csgclaw-cli bot list --role worker --channel <current_channel>
```

If `bot list` shows the needed worker/bot but its status is `unavailable`, do not treat it as reusable yet.
Run `bot create` again with the same original parameters, including the same `--id`, `--name`, `--role`, `--channel`, and any original `--description` or model options.
This recreates the missing underlying agent for that bot so it can become `available`.

Create a worker:

```bash
csgclaw-cli bot create --id u-alex --name alex --role worker --channel <current_channel>
```

Add a worker into the given room:

```bash
csgclaw-cli member create --room-id oc_xxx --user-id u-alex --inviter-id u-mananger --channel <current_channel>
```

Verify all required workers are in the room before tracking:

```bash
csgclaw-cli member list --room-id oc_xxx --channel <current_channel>
```

## Tracking Script Usage

```bash
cd ~/.picoclaw/workspace/skills/manager-worker-dispatch
python scripts/manager_worker_api.py start-tracking -h
python scripts/manager_worker_api.py stop-tracking -h
```

Start tracking todo:

```bash
python scripts/manager_worker_api.py start-tracking --channel <current_channel> --room-id room-123 --todo-path ~/.picoclaw/workspace/projects/demo/todo.json
```

Stop the tracking:

```bash
python scripts/manager_worker_api.py stop-tracking --todo-path ~/.picoclaw/workspace/projects/demo/todo.json
```

If you need to direct the human user to the project files on their Mac, point them to the host-side path such as `~/.csgclaw/projects/demo/todo.json`, not the in-box `/home/picoclaw/...` path.

## Operating Rules

- Reuse available workers before creating new ones.
- If a matching worker is listed as `unavailable`, recreate it with `bot create` and the original parameters so the backing agent is created before joining or dispatching work to it.
- Before writing the final `todo.json` or running `start-tracking`, verify all required workers are already members of the target room with `csgclaw-cli member list`.
- Treat missing room membership as a blocker: add the worker, verify again, and only then continue with `todo.json` and tracking.
- Keep `todo.json` aligned with the actual assignment being dispatched.
- Do not casually reorder tasks in the sequential flow.
- Let `start-tracking` drive dispatch from `todo.json`; do not duplicate that logic in manual room-message procedures.
- While tracking is active, do not manually tell the next worker to start in prose. The tracker is the only sequencer.
- When a worker finishes, they must reply in the shared room with a normal summary or blocker note; updating `todo.json` alone does not release the next task.
- Use `csgclaw-cli` for all non-tracking operations; do not use `scripts/manager_worker_api.py` for room, bot, member, worker listing, worker creation, or messaging operations.
- If `start-tracking` or `stop-tracking` response shape differs from expectations, patch the script instead of improvising around it.
