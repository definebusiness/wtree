Before I give you the deliverable, one transparency note: the live search results I received were for *other* worktree utilities (FatDoge's `wtree` UI tool , `wt-cli` , `wtp`, `worktree-cli` , `git-worktree-cli` , `timvw/wt` , `taecontrol/wt` ) and general worktree philosophy pieces . None of them returned the actual `definebusiness/wtree` documentation, so my understanding of *that* project is built from the three specification/idea files you attached, which reference its core specs (`wtree.spec.md`, `nested-mount-ignore-management.md`). If you can paste the repo's README or `wtree.spec.md`, I can tighten this further. What I have is more than enough to reason about the model and the gaps.

---

## What wtree is (consolidated understanding)

**wtree is a multi-repository project manager that gives you a composed, cloneable "super-project" made of *independent* Git repositories — deliberately avoiding both submodules and subtrees.**

Core model, as established by the attached specs:

- A project is a **graph of repositories**: one root (mount `.`) plus nested children, each with a **parent-relative mount**, and **repository IDs that are independent of filesystem paths**  .
- Nested repositories stay **ordinary, independent Git repos**. There is **no `.gitmodules`, no gitlink, and no merged history** — this is stated as an explicit non-goal .
- Each child mount must be **ignored by its immediate parent's committed `.gitignore`** (the nested-mount-ignore rules), enforced at clone time .
- Two configuration layers: machine-local, ignored **`.wtree.yml`**, and tracked, portable **`project.wtree.yml`** that must never carry local paths, credentials, or machine state .
- **`wtree init`** discovers each repo's branch/upstream/remote/URL and initial commits and publishes the portable manifest as one transaction; it never stages, commits, tags, or pushes .
- **`wtree clone <manifest-source> [dest]`** reconstructs the entire tree parent-first from a local or HTTP(S) manifest, verifying remote, branch, mount, initial-commit identity, clean state, and the *byte-identical* tracked manifest — all transactionally with staging + atomic rename + rollback .
- **Workspaces** provide parallel branch development across the whole tree: `create` (new branch + worktree everywhere), `checkout` (existing branches), plus `status`, `list`, `repo get`, `doctor`, `import`, `remove`, `delete`, with strict preflight-first, transactional, recovery-record semantics .

**In-implementation / planned features from your documents:**

1. **Portable manifest clone** (status: planned) — the `init`/`clone` pipeline above .
2. **Immutable release lock manifests** (status: initial idea) — freeze one verified workspace into `project.wtree.lock.yml`, anchored by the outer repo's release tag, binding each child to a full object ID + fetch source + identity, for reproducible releases without a monorepo or submodules .
3. **Allowing missing branches** (status: initial idea) — explicit *mixed* and *partial* workspaces, remote-materialization of tracking branches, per-repository fallbacks, all fully planned and transactional .

The through-line: **wtree wants the composition/pinning benefits of submodules and the "clone-once-get-everything" benefit of subtrees, minus detached-HEAD confusion, forgotten submodule pushes, recursive-command friction, and history bloat.**

---

Below is the requested suggestions document.

---

# Suggestions: Closing the gap to a full submodule + subtree experience — without the hassle

## 0. Framing

Git submodules and git subtrees each solve part of "compose several repos into one project," and each brings its own pain:

| Capability users actually want | Submodules give it via… | Subtrees give it via… | The hassle |
|---|---|---|---|
| One command to get the whole project | `clone --recurse-submodules` | history is already inside | detached HEADs; `--init --recursive` footguns / vendored history bloat |
| Exact reproducible pin of every part | gitlink commit | embedded commit | forgotten submodule pushes; hard subtree `split` |
| Run/build/fetch across all parts | `git submodule foreach` | n/a (it's one repo) | recursive command complexity |
| Contribute changes upstream easily | push each submodule | `subtree push` | easy to forget; `split` is arcane |
| Consumers unaware of the plumbing | — | yes, it's transparent | subtree merges confuse contributors |

wtree already covers the **composition + reconstruction** axis (`init`/`clone`)  and has a credible plan for the **exact-pin** axis (release locks) . The suggestions below fill the remaining gaps so the *combined* experience is complete. They are ordered by how directly they close a submodule/subtree capability.

---

## 1. `wtree update` / `wtree sync` — the missing submodule-update equivalent (highest priority)

The portable-manifest spec explicitly lists `update` and `sync` as out of scope , and the release-lock idea repeatedly assumes a future `sync` exists . This is the single biggest gap versus `git submodule update --init --recursive`.

**Suggested behavior:**
- Reconcile an *existing* checkout against a *changed* `project.wtree.yml`: clone newly-added repositories, re-mount moved ones, and flag (never silently delete) removed ones.
- Fetch/fast-forward each repo's tracked default branch parent-first, reusing the existing verification set (remote, upstream, mount, identity, clean state) already defined for clone .
- Keep the same preflight-first, staging, atomic, rollback-with-recovery-record contract as clone .
- A `--lock <path>` mode that materializes exact object IDs from a release lock instead of following branches  — this is where the submodule "pinned" feel and the release model meet.

This turns wtree from "clone once" into "keep the composition current," which is what makes submodules usable day-to-day.

## 2. Recursive execution: `wtree foreach` / `wtree exec`

The direct analogue of `git submodule foreach`, which none of the composition primitives make ergonomic.

**Suggested behavior:**
- Run a command in every repository of a workspace, in deterministic parent-first (and reverse for teardown) order.
- Expose the same identity variables as submodule foreach (repo ID, mount, path, branch, resolved commit), sourced from workspace state rather than inferred from directory names — consistent with wtree's rule that identity never comes from paths .
- `--json` per-repo result envelopes and non-zero aggregate exit on any failure, matching wtree's existing machine-output discipline .

## 3. Aggregate read commands: recursive `status`, `fetch`, `pull`

`status` already exists and must distinguish synchronized/mixed/partial workspaces . Extend the aggregate idea to fetch/pull-style reads:

- **`wtree fetch`** — read-only `git fetch` across all repos, reporting per-repo ahead/behind, mirroring the read-only `ls-remote` planning wtree already performs during clone .
- **`wtree status`** should also surface an "outer manifest drift" signal: repositories present on disk but absent from `project.wtree.yml` (and vice-versa), which is the raw material `update` (§1) acts on.
- Keep pulls opt-in and per-repo-fast-forward-only by default to avoid the classic submodule "silently on a detached commit" trap.

## 4. Promote release lock manifests from idea to spec (the submodule-pin experience)

The release-lock idea  is exactly the "exact reproducible pin of every part" that submodule gitlinks provide, but cleaner because it's an explicit metadata layer with no gitlink and no history merge. Recommend fast-tracking it, and resolving its open questions in this order:

1. **Filename policy** — start with a stable root `project.wtree.lock.yml` anchored by the outer tag (Q1) ; add `releases/<tag>.wtree.lock.yml` only if multiple locks must coexist on one branch.
2. **Object-format neutrality** — decide the SHA-1/SHA-256 representation now (Q7) so the schema isn't retrofitted later .
3. **Materialization command** — reuse `wtree clone --lock` and `wtree update --lock` rather than inventing a parallel path (Q8) , keeping one repository model.
4. **Remote-availability proof** — verify every locked object is fetchable *before* the outer tag is published, without making local lock generation network-dependent (Q4) .

This gives the auditable, reproducible release identity submodules offer, without the "I forgot to push the submodule so the gitlink points at a commit no one else has" failure mode.

## 5. `wtree push` — coordinated upstream contribution (the subtree-push win, safely)

Because wtree keeps each child as an independent repo, contributing upstream is already just `git push` in that repo — this is *inherently* better than `git subtree push --prefix`. But the classic submodule hassle is *forgetting* to push a dependency before the parent. Suggest a thin, preflight-heavy helper:

- **`wtree push`** with a plan that lists, per repo, ahead/behind vs. its tracked upstream and refuses to report success while any repo has unpushed commits its siblings/parent depend on.
- Never auto-push as a side effect (consistent with wtree's rule that `init` never pushes  and lock generation stays local/non-publishing ).
- This makes "did I forget to push a part?" — the number-one submodule complaint — impossible to miss.

## 6. Migration/adoption tooling: import existing submodule and subtree projects

Adoption of existing directories is currently a clone non-goal , and `import` today only infers workspace state from existing checkouts . To make wtree a *replacement* for submodules/subtrees rather than a greenfield-only tool, add:

- **`wtree adopt --from-submodules`** — read `.gitmodules`, map each submodule to a wtree repository (URL, mount, default branch), generate `project.wtree.yml`, and add the required parent `.gitignore` mount rules , while explicitly *not* touching the submodule histories.
- **`wtree adopt --from-subtree --prefix <dir>`** — recover the upstream URL and split point of a subtree'd directory into a proper independent nested repo.
- Both must be read-only-until-transaction and produce a dry-run plan first, matching wtree's established safety model .

## 7. Lifecycle hooks: `post-clone`, `post-checkout`, `post-update`

A huge part of the *un-hassle* promise is "after I get the tree, it just works." Neither submodules nor subtrees run project setup for you.

- Add optional, opt-in hooks declared in the portable manifest (e.g. per-repo or project-level) that run after clone/checkout/update — for dependency install, codegen, etc.
- Keep them **explicit and sandboxed**: never run automatically without the manifest declaring them, run under the same hardened non-interactive environment wtree already mandates for Git operations , and include them in dry-run output as planned actions, never executed on `--dry-run`.

## 8. Selective materialization (subtree's "just this part" + submodule sparse/shallow)

Currently sparse/partial/shallow clone and LFS orchestration are non-goals . The **partial workspace** idea  already introduces the concept of intentionally-absent repositories with clear hierarchy rules for mounting descendants when a parent is omitted. Recommend converging them:

- Let `clone`/`update` accept a repository-ID selection so large or optional components can be skipped, reusing the partial-workspace state model (`workspace kind`, intentionally-omitted IDs) rather than a second mechanism .
- Add shallow/partial-clone **as a transport option only** (depth, blob filter), keeping identity verification honest — i.e., initial-commit identity checks  may need a documented relaxation under shallow, which should be an explicit, recorded workspace attribute rather than a silent downgrade.
- Sequence LFS as a follow-up once hooks (§7) exist, since LFS is often just a post-clone step.

## 9. Flexible URL resolution: relative URLs and named profiles

Relative repository URLs and named URL profiles are current non-goals  . Submodules' relative-URL feature is genuinely useful for org mirrors, forks, and internal registries. Suggest:

- Optional **URL profiles / base substitution** resolved at clone/update time, while the manifest continues to store only credential-free, reviewable URLs  and the persisted normalized source stays credential-free .
- Keep the strict rejection of userinfo, control characters, and credential-bearing forms exactly as specified  — profiles resolve to allowed URL shapes, they don't loosen validation.

## 10. Mature the mixed/partial workspace model (heterogeneous composition)

Endorse the recommended incremental direction in the allow-missing-branches idea essentially as written :

1. Keep strict all-repository synchronization the default.
2. Add unambiguous remote-materialization of tracking branches.
3. Add explicit per-repository fallback mappings for mixed workspaces.
4. Treat "create missing branch" and "omit repository" as separate, clearly-labeled advanced policies rather than one vague `--allow-missing` flag .

Crucially, carry the `synchronized | mixed | partial` workspace-kind through `status`, `list`, `repo get`, `doctor`, and JSON so heterogeneous state is never mistaken for drift  — and so `delete` respects branch provenance (never deleting shared `main`/fallback branches) .

## 11. `doctor` and drift coverage for the new surfaces

Every feature above adds state that can drift. Extend the existing `doctor` (which the specs already position as the arbiter of intentional-omission vs. drift ) to also validate:

- lock-vs-checkout object-ID mismatches ;
- manifest-vs-disk repository set drift (the input to `update`, §1);
- missing parent `.gitignore` mount rules on already-cloned trees (clone enforces this only at creation today) ;
- hook and profile configuration validity.

---

## Suggested delivery sequence

| Phase | Items | Rationale |
|---|---|---|
| 1 — Ship the composition loop | §1 `update`/`sync`, §11 doctor drift | Without `update`, wtree is "clone-once"; this is the biggest submodule gap  |
| 2 — Daily ergonomics | §2 `foreach`, §3 aggregate reads, §5 `wtree push` | Removes the recursive-command and forgotten-push pain that defines submodule frustration |
| 3 — Reproducibility | §4 release locks → spec | Delivers the submodule-pin experience, cleanly  |
| 4 — Adoption & convenience | §6 migration, §7 hooks | Lets existing submodule/subtree projects move in and "just work" |
| 5 — Scale & flexibility | §8 selective/shallow, §9 URL profiles, §10 mixed/partial | Matches submodule sparse/relative-URL power and subtree partial-materialization  |

## Non-negotiable invariants for all of the above

Every suggestion must preserve wtree's existing guarantees, or it will reintroduce the very hassle it aims to remove:

- **No `.gitmodules`, no gitlinks, no merged histories** — the metadata-layer-over-independent-repos model is the whole point .
- **Preflight-first, transactional, staging + atomic rename, full rollback with recovery records** on any mutation .
- **Identity never inferred from paths**; repository IDs and recorded initial commits remain authoritative  .
- **Credential-free, machine-state-free portable manifests**, with strict URL and manifest-source validation retained .
- **No silent substitution** of a branch tip for a pinned object, and no silent fallback under default checkout  .
- **`--dry-run` and `--json` parity** for every new command, with human stderr empty in JSON mode .

---

If you'd like, I can turn any single section (most usefully §1 `update`/`sync` or §4 the release-lock schema) into a full `.spec.md` draft in the same house style as your attached files. If you can share `wtree.spec.md` and `nested-mount-ignore-management.md`, I'll align terminology and command surfaces exactly.