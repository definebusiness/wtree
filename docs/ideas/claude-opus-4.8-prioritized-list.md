## P0 — Load-bearing gaps (must exist for the model to work day-to-day)

**1. `wtree update` / `wtree sync`**
*What:* Reconcile an existing checkout against a changed `project.wtree.yml` (clone new repos, re-mount moved ones, flag removed ones) and fast-forward each repo's tracked default branch parent-first, reusing clone's verification set (remote, upstream, mount, identity, clean state) and its transactional rollback contract.
*Why necessary:* The portable-manifest spec explicitly lists `update` and `sync` as out of scope , yet the release-lock idea repeatedly assumes a future `sync`/`clone --lock` exists to materialize revisions . Without it, wtree is "clone-once" and cannot keep a composition current — the core thing submodules do every day.

**2. Portable manifest clone (`init` + `clone`)**
*What:* Publish a portable, credential-free `project.wtree.yml` via `init`, and reconstruct the whole tree via `clone` with read-only planning, staging, atomic rename, and full rollback .
*Why necessary:* This is the "clone-once-get-everything" benefit of subtrees and the composition backbone every other feature builds on. It is already fully specified as **planned** , so it is the first thing to actually implement.

**3. Extended `doctor` drift detection**
*What:* Validate manifest-vs-disk repository drift (the input `update` acts on), missing parent `.gitignore` mount rules on already-cloned trees, and later lock/hook/profile validity.
*Why necessary:* `doctor` is already positioned as the arbiter of intentional-omission versus drift . Every new feature adds state that can drift; without `doctor` coverage, `update` (item 1) has no safe way to distinguish "user removed a repo" from "checkout broke."

---

## P1 — Daily ergonomics (remove the defining submodule pain)

**4. `wtree foreach` / `wtree exec`**
*What:* Run a command across every repo in a workspace in deterministic parent-first order, exposing repo ID, mount, path, branch, and commit from workspace state — not inferred from directory names — with `--json` per-repo results.
*Why necessary:* Direct analogue of `git submodule foreach`. wtree's rule that identity never comes from paths  makes this cleaner than submodules, but the capability itself is missing.

**5. Aggregate reads: recursive `fetch` and drift-aware `status`**
*What:* Read-only `git fetch` across all repos with per-repo ahead/behind, plus a `status` that surfaces outer-manifest drift (repos on disk but absent from the manifest, and vice-versa).
*Why necessary:* Reuses the read-only `ls-remote` planning clone already performs  and feeds `update`. Gives users the "where does everything stand" view submodules make awkward.

**6. `wtree push` (coordinated upstream contribution)**
*What:* A preflight-heavy helper that reports per-repo ahead/behind vs. upstream and refuses to report success while any repo has unpushed commits its siblings/parent depend on. Never auto-pushes.
*Why necessary:* Because each child stays an independent repo, contributing upstream is already better than `git subtree push`. But the #1 submodule failure — "I forgot to push the dependency, so the pin points at a commit no one else has" — must be made impossible to miss. Consistent with wtree's rule that `init` never pushes .

---

## P2 — Reproducibility (the clean submodule-pin experience)

**7. Release lock manifests (promote from idea to spec + implement)**
*What:* Freeze one verified workspace into an immutable `project.wtree.lock.yml`, anchored by the outer repo's release tag, binding each child to a full object ID + fetch source + identity, materialized via `wtree clone --lock` / `wtree release verify` .
*Why necessary:* Delivers submodule-gitlink reproducibility without gitlinks or merged history . Currently only an **initial idea**; its open questions (lock filename policy Q1, SHA-1/SHA-256 neutrality Q7, which command materializes Q8, remote-availability proof Q4)  must be resolved so the schema isn't retrofitted later.

**8. Mixed/partial workspace model — remote materialization + fallbacks**
*What:* Teach `checkout` to materialize a local tracking branch from an unambiguous remote-tracking branch, then add explicit mixed-branch mode via per-repository fallback mappings, carrying a `synchronized | mixed | partial` workspace kind through `status`, `list`, `repo get`, and `doctor` .
*Why necessary:* Real projects have features that touch only one repo . This adds normal Git-like remote checkout and heterogeneous composition without weakening the safe default — following the doc's own recommended incremental sequence . It also lets `delete` respect branch provenance and never delete shared `main`/fallback branches .

---

## P3 — Adoption & convenience (make it a real submodule/subtree replacement)

**9. Migration tooling: `adopt --from-submodules` / `--from-subtree`**
*What:* Read `.gitmodules` (or a subtree `--prefix`) and generate `project.wtree.yml` plus the required parent `.gitignore` mount rules, dry-run first, without touching the original histories.
*Why necessary:* Destination adoption is currently a clone non-goal , so existing submodule/subtree projects cannot move in. Without migration, wtree is greenfield-only rather than a replacement.

**10. Lifecycle hooks: `post-clone` / `post-checkout` / `post-update`**
*What:* Optional, opt-in hooks declared in the manifest, run under the same hardened non-interactive Git environment wtree already mandates , shown in dry-run and never executed on `--dry-run`.
*Why necessary:* A core "un-hassle" promise is "after I get the tree, it just works." Neither submodules nor subtrees run project setup; hooks close that gap for dependency install/codegen and are a prerequisite for LFS orchestration later.

---

## P4 — Scale & flexibility (match submodule sparse/relative-URL power)

**11. Selective + shallow/partial materialization**
*What:* Let `clone`/`update` accept a repository-ID selection to skip large/optional components, reusing the partial-workspace state model (workspace kind + intentionally-omitted IDs) , plus shallow/partial-clone as a transport option with an explicitly recorded relaxation of initial-commit identity checks .
*Why necessary:* Matches submodule shallow/sparse and subtree "just this part," and converges cleanly with the already-designed partial workspace hierarchy rules  rather than inventing a second mechanism. Currently a non-goal .

**12. Flexible URL resolution: relative URLs and named profiles**
*What:* Optional URL profiles / base substitution resolved at clone/update time, while the persisted manifest source stays credential-free and validation still rejects userinfo and credential-bearing forms .
*Why necessary:* Submodules' relative-URL feature is genuinely useful for org mirrors and forks; both are current non-goals  . Profiles resolve to allowed URL shapes without loosening the strict validation the spec already enforces.

---

### Cross-cutting invariants every item must preserve
No `.gitmodules`, gitlinks, or merged histories ; preflight-first, transactional, staging + atomic rename with recovery records on any mutation ; identity never inferred from paths  ; credential-free, machine-state-free manifests ; no silent branch-tip substitution for a pinned object and no silent default-branch fallback  ; and `--dry-run` + `--json` parity with empty human stderr in JSON mode for every new command .

If you'd like, I can expand the top item (P0 #1, `update`/`sync`) or P2 #7 (the release-lock schema) into a full `.spec.md` draft in the same house style as your attached files.