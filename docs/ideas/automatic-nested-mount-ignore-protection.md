# Idea: automatically protect nested repository mounts

Status: specified
Specification: [Automatic nested mount ignore protection specification](../spec/automatic-nested-mount-ignore-protection.md)
Supersedes: [Nested mount ignore management specification](../spec/nested-mount-ignore-management.md); [Nested mount ignore management implementation plan](../plans/nested-mount-ignore-management.md)

## User story

As a developer using nested Git repositories, I want `wtree` to ensure that
every nested repository mount is ignored by its immediate parent repository so
that `git add .` cannot accidentally stage the nested repository as an embedded
repository.

## Essential behavior

- Protection is automatic during `wtree init` and `wtree create`; there is no
  separate command or opt-in flag.
- For every non-root repository, derive one literal anchored directory rule
  from the same normalized parent-relative mount used for workspace placement:
  `/<mount>/`.
- Reject a mount that cannot be represented as one safe Git-ignore pattern.
- Write the rule to the immediate parent's root `.gitignore`. Never write it to
  a project-wide file or to the child repository.
- Treat a mount as already protected only when `git check-ignore` reports an
  effective rule from a `.gitignore`; local and global excludes do not count.
- Refuse a symlink or non-regular `.gitignore`. Create a missing file or append
  to a regular file without changing its existing content, order, newline
  style, or permissions.
- Replace each changed file atomically. Retain successful changes if a later
  file fails, report what remains, and make retrying safe and duplicate-free.
- During init, protect source parent repositories before publishing wtree
  configuration or state.
- During create, protect each newly created parent worktree and verify the
  effective rule with Git before adding any direct child worktree.
- Never stage or commit `.gitignore`; report every changed file for user review
  and commit.
- Existing dry-run modes show the required changes without touching files,
  locks, branches, worktrees, or state.

## Deliberate simplifications

- No `wtree add-ignore` command.
- No `--add-ignore` flag.
- No committed-base ignore reconstruction or special workspace-plan schema.
- No cross-file rollback of successful source `.gitignore` updates.
- No cleanup or removal of stale ignore rules.

## Acceptance

- A three-level repository hierarchy writes each rule to the correct immediate
  parent and `git check-ignore` confirms every mount before the child is used.
- Repeating init or create does not append a rule when the mount is already
  effectively ignored by a `.gitignore` file.
- If another `.gitignore` rule still makes a mount visible after the literal
  root rule is appended, verification fails and the child is not initialized
  or added until the conflict is resolved.
- A write or verification failure prevents the affected child worktree from
  being added, leaves every `.gitignore` as a complete old-or-new file, and
  reports retained and remaining changes.
- After a successful workflow, `git add .` in every parent cannot stage a
  managed nested repository as a `160000` gitlink.
