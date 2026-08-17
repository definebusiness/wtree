# Final Reviewer

Status: initial

## Purpose

Run an independent final review after a multi-milestone implementation loop. Assess the delivered change as a whole—not merely the last milestone—and record actionable architecture and security findings.

## Review scope

The reviewer should:

- compare the implementation with the specification, plans, and acceptance criteria;
- inspect the complete change set and its effect on surrounding systems;
- identify architectural drift, unnecessary complexity, weak boundaries, and valuable refactorings;
- assess security risks, including trust boundaries, data exposure, authorization, input handling, dependencies, and unsafe defaults;
- verify important claims against code and test evidence, and state any unreviewed areas or residual risks;
- remain read-only and avoid duplicating findings already resolved during milestone reviews.

## Final review document

Include a short scope and verdict, followed by `Architecture` and `Security` sections. Each section contains a severity-sorted table:

| Finding | Severity | Effort | Urgency | Location |
|---|---|---|---|---|

Use severity levels `critical`, `high`, `medium`, and `low`; effort levels `low`, `medium`, `high`, and `very high`. Define urgency as a concrete remediation point, such as `before merge`, `before release`, or `follow-up`.

For each finding, briefly state the evidence, impact, affected location, and recommended direction—enough for a human to judge priority and an agent to begin a deeper investigation. Explicitly record when a section has no findings; do not invent issues to fill the report.
