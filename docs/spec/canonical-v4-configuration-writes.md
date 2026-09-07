# Canonical v4 configuration writes specification

Status: planned
Source idea: none (created directly)
Implementation plan: [Canonical v4 configuration writes implementation plan](../plans/canonical-v4-configuration-writes.md)
Related specifications: [Portable manifest v2 base-repository format specification](portable-manifest-v2-base-repository-format.md); [Local and shared workspace lifecycle hooks specification](local-workspace-lifecycle-hooks.md); [Companion repositories specification](companion-repositories.md); [Immutable release lock manifests specification](release-lock-manifests.md)

## 1. Purpose

`wtree` currently chooses the lowest configuration version that can represent
the fields being written. That policy was useful while backward writer
compatibility mattered, but it now leaves newly created projects on version
two and upgrades them incrementally as features are added.

This specification establishes version four as the single current write
format for local project configurations and portable manifests. Versions two
and three remain supported input formats. Version one remains unsupported.

This document changes only version-selection and migration timing. It does not
make hooks or companion repositories mandatory, add fields to version four,
or authorize execution of portable or shared hooks.

## 2. Relationship to existing contracts

This specification replaces earlier statements that ordinary init and clone
emit version two, that hook management upgrades version two only to version
three, or that update republishes an older local version unchanged. The
remaining schema, trust, transaction, rollback, identity, and exact-manifest
contracts in the related specifications stay in force.

Local `.wtree.yml` and tracked `project.wtree.yml` remain independent document
types with independent version fields. They merely share version four as
their current authoring version.

Workspace state, the project registry, release locks, recovery records, and
public command result schemas keep their own versions and are outside this
policy.

## 3. Version vocabulary

The implementation must distinguish these concepts:

- **supported read versions** are versions two, three, and four;
- **unsupported version one** is rejected and never translated;
- **current write version** is version four; and
- **explicit legacy encoding** is the ability of low-level configuration
  helpers and tests to encode a caller-supplied version-two or version-three
  value without silently changing its version.

Constants and comments must make the distinction clear. A legacy schema
constant must not also be described or used as the default writer version.
Service code that owns a new or replacement generation selects the current
write version explicitly before canonical encoding.

## 4. Read compatibility and strictness

### 4.1 Accepted inputs

Local project configuration and portable manifest readers must continue to
accept valid versions two, three, and four. Each version is decoded under its
own strict schema:

- version two rejects hook fields and companion repository fields;
- version three accepts hook fields but rejects companion repository fields;
- version four accepts the complete current field set; and
- unknown fields and invalid values remain errors in every version.

A version-four document is valid without `hooks`, `shared_hooks`, or any
repository marked `companion`.

### 4.2 Version one

Local version one remains rejected with the existing reinitialization
guidance. Portable version one remains rejected as an unsupported portable
manifest. No automatic or explicit version-one migration is introduced.

### 4.3 Reads are non-mutating

Loading or inspecting a version-two or version-three document must not rewrite
it. This includes read-only commands such as configuration get/list, status,
doctor (including fixes that do not otherwise own the configuration file),
planning, inventory, and dry-run paths.

Decoding into an in-memory representation does not itself authorize a write.
Repeated reads preserve file bytes, permissions, timestamps, and Git state.

## 5. New generation policy

Every newly authored configuration generation uses version four even when it
contains no hooks or companion repositories.

| Operation | Newly authored output | Required version | Preserved input |
|---|---|---:|---|
| `wtree init` | local `.wtree.yml` and portable `project.wtree.yml` | 4 | Existing preflight and rollback generations |
| `wtree clone` | local `.wtree.yml` in the cloned project | 4 | The tracked portable manifest remains byte-identical to the selected source |
| release materialization | local `.wtree.yml` in the materialized release | 4 | The tracked portable manifest and release lock remain exact authenticated inputs |
| update publication | replacement local `.wtree.yml` | 4 | The candidate tracked manifest remains the exact Git-owned candidate generation |

Init planning, dry-run output, and execution must agree on the version-four
bytes they would publish. The init project ID must remain stable for identical
repository identity facts; changing the output format must not accidentally
create a new project identity algorithm.

Clone and release materialization consume a portable manifest already tracked
by the selected repository commit. They do not author a replacement portable
manifest and therefore must not change its version or bytes. A version-two or
version-three manifest can consequently produce a version-four local config.

Update publication likewise preserves its existing exact candidate-manifest
contract. Moving the base checkout to a commit containing a version-two or
version-three manifest is not permission to edit that commit's file. The
local configuration generation written by update is version four.

## 6. Upgrade-on-write policy

When a successful operation already owns replacement of a local project
configuration or portable manifest for a product reason, that replacement is
encoded as version four. The operation preserves all fields valid in the
source document unless its own contract intentionally changes them.

The policy applies at least to:

- project-scoped configuration set and unset when they write `.wtree.yml`;
- update publication of `.wtree.yml`;
- successful `hooks install` changes to `.wtree.yml`;
- successful `hooks share` changes to `project.wtree.yml`; and
- repository baseline mutations that replace either document.

The implementation must inventory all production callers that encode or
replace either document so a legacy version cannot be republished by an
unlisted write path.

This policy does not turn a read or a semantic no-op into a migration command.
Where an operation already guarantees byte-identical no-op behavior, that
guarantee remains: for example, a hook install/share that changes nothing does
not rewrite merely to raise the version. This specification does not otherwise
change an existing command's definition of whether a requested mutation is a
no-op.

Low-level marshal helpers remain capable of explicit legacy encoding. They do
not silently mutate the caller's version, because readers, fixtures, identity
derivation, exact-byte verification, and compatibility tests still need to
represent legacy documents. Upgrade selection belongs to the service write
boundary.

## 7. Data preservation and safety

Upgrading a generation changes its version and canonical YAML representation,
but must preserve all unrelated semantic values, including:

- project identity, name, and base repository;
- repository topology, sources, clone/upstream data, identity roots, mounts,
  default branches, and companion roles;
- worktree and discovery settings;
- manifest metadata; and
- local, portable, and shared hook declarations, including order and explicit
  values.

Version migration does not grant hook consent. Shared hooks remain inert,
portable `post-clone` execution still requires the existing invocation-scoped
authorization, and local hooks execute only under their existing lifecycle
contracts.

All existing atomic replacement, locking, generation revalidation, rollback,
file-mode, secret-redaction, and platform-portability requirements remain in
force. A failed write restores or retains the exact pre-operation generation
according to the owning operation's current transaction contract.

## 8. User-visible behavior and documentation

User documentation and examples must describe version four as the format
written by current `wtree`. Compatibility documentation must separately state
that versions two and three are still readable and version one is rejected.

No standalone migration command is required. Users upgrade naturally the next
time an operation legitimately replaces a configuration generation.

## 9. Acceptance criteria

1. Strict readers accept valid local and portable versions two, three, and
   four, reject version one, and preserve each version's field restrictions.
2. Version four validates and serializes successfully without hooks, shared
   hooks, or companion repositories.
3. Fresh init writes version four to both `.wtree.yml` and
   `project.wtree.yml`, including dry-run/planning representations.
4. Clone from version-two, version-three, and version-four manifests writes a
   version-four local config while preserving the tracked manifest's exact
   bytes and version.
5. Release materialization from each supported manifest version writes a
   version-four local config while preserving exact manifest and lock
   verification.
6. Update publication from supported current/candidate version combinations
   writes a version-four local config, preserves local hooks and unrelated
   values semantically, and keeps the exact candidate manifest contract.
7. Every actual local/portable configuration replacement upgrades version two
   or three to version four, while existing no-op and failure paths remain
   byte-identical.
8. Read-only commands and dry runs do not migrate files or alter bytes, modes,
   timestamps, registry/state data, or Git state.
9. Deterministic init identity, hook consent, locking, rollback, recovery,
   credential redaction, and Linux/macOS/Windows behavior remain compatible.
10. Tests and documentation use unambiguous legacy-read and current-write
    terminology; no production default still selects the lowest representable
    version.

## 10. Non-goals

This specification does not add:

- a version-one reader or migration path;
- a standalone `wtree migrate` command;
- automatic rewrites during load, discovery, status, doctor, or startup;
- mandatory hooks, shared hooks, or companion repositories in version four;
- rewriting of Git-owned manifests consumed by clone, release
  materialization, or update; or
- changes to workspace-state, registry, lock, journal, recovery, plan, or
  result schema versions.
