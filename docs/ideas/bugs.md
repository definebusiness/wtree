# Recorded bugs

Status: initial

## Checkout and recovery

- [ ] [Stale worktree registration produces an unexplained checkout conflict](bug-stale-worktree-checkout-conflict.md).
  Captured Git evidence, deterministic reproduction, diagnostic fix boundaries,
  and regression criteria; reported 2026-09-20. No product fix implemented.
- [ ] [Stale default HEADs and orphaned removal recovery have no actionable repair](bug-stale-default-state-and-removal-recovery.md).
  Captured recovery record, separate and combined reproductions, operation-label
  defect, reconciliation requirements, and safety tests; reported 2026-09-20.
  Local metadata was repaired, but no product fix was implemented.

## Documentation

- [x] Clearly explain and illustrate both supported root layouts in the root
  README.md (ordinary directory and Git repository roots with a v2 manifest),
  and qualify the metadata ownership statement.
  Fixed in [README.md](../../README.md#what-it-does), including initialization
  examples for both layouts in [Usage](../../README.md#usage).
