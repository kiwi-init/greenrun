---
name: greenrun
description: Run and diagnose the repository's GitHub Actions locally after code changes.
---

# Greenrun

Use Greenrun after making code changes when the repository contains
`.github/workflows`.

1. Run `greenrun --plain`.
2. Treat exit `0` as locally passed, `1` as a CI failure, `3` as partial
   verification, `2` as a Greenrun/runtime error, and `130` as cancelled.
3. Read the compact result printed by the command. Use
   `greenrun show latest` if it is no longer in context.
4. On failure, inspect only the relevant evidence with
   `greenrun logs latest --failed`.
5. Fix the failure and rerun `greenrun --plain`.
6. Never describe `partial` as green. Report which jobs are blocked or
   `remote_only`.
7. Do not provide model API keys to Greenrun. It does not use an LLM.

For a failed hosted run, use `greenrun github latest-failed`, then inspect it
with the same `show` and `logs` commands.
