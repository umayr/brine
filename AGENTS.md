# Brine

- Brine owns the operator UI/API, workspaces, Git integration, target enrollment and display of deployment results. Dill owns the Pkl schema, planning and execution. Do not reimplement Dill operations or infer success when the target reports an unknown outcome.
- Document the Dill version actually bundled in `Dockerfile`. Do not claim support for newer features without testing the pair.
- Keep authentication, CSRF, ownership checks and protected-state classification on every affected path. Missing or unreadable state must not enable a legacy mutation. Retired local Alpha and strict-secret execution are not fallbacks.
- Remove obsolete implementations and instructions; Git history is the archive. Preserve denial tests and live recovery contracts.
- Work in an isolated Git worktree. Run `make build test vet`; for UI changes, exercise the affected browser flow. Use synthetic data and disposable targets, never live homelab stacks.
- Before dispatch or resuming a worktree, verify the intended base and current project instructions. Give each worker the exact files and acceptance check; use global roles, not project model overrides.
- The lead integrates and verifies the operator flow on the pinned Brine/Dill pair. Record passed with evidence, blocked with an owner and next action, or not run. PR counts and iteration limits are not acceptance.
