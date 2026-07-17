---
name: ship
description: Branch, commit, and/or open a PR for the current changes — driven by AskUserQuestion. Use when the user wants to commit their work, create a branch, open a pull request (draft or ready), refine an existing PR's description, set the release label, or "ship" / "wrap up" what they've been working on. Produces conventional commits and a PR that fits the repo template and passes the PR Checks gate.
---

# Ship: branch → commit → PR

Drives the end-of-work flow for wsm. Everything is opt-in through
`AskUserQuestion` — never assume; ask, then do only what was chosen. There are up
to **two** ask rounds: the first (§2) settles the workflow (branch, commits, PR,
release label); the second (§5.0) settles PR *presentation* (title + description
style) and fires only when a PR body is being written. Commits and PRs must
satisfy the repo's devex gate (`.github/workflows/pr-checks.yml`):
Conventional-Commit titles and commits, a real Summary, completed requirement
checkboxes, and **exactly one `release:*` label**.

## 1. Read state first (before asking)

Gather the context the questions depend on — do this silently:

```sh
git rev-parse --abbrev-ref HEAD        # current branch
git status --porcelain                 # what's staged/unstaged/untracked
git diff --stat HEAD                    # rough shape of the change
gh pr view --json number,url,title,isDraft,baseRefName,labels 2>/dev/null  # existing PR?
gh api user --jq .login                 # your GitHub username, for the branch prefix
```

Note the **current branch** and whether it's the default (`main`). Skim the diff
enough to know what the change *is* — you need it to name the branch, write the
commit(s), and fill the PR. Do not read every file; read enough to summarize.

The `gh pr view` call tells you whether the current branch **already has an open
PR** (non-empty output = yes; note its number, draft state, base, and any
existing `release:*` label). This flips the "Pull request" question from *create*
to *update* (see §2). When a PR already exists, branching off usually doesn't make
sense — lean toward staying on the current branch so the commits land on the open PR.

## 2. Ask: workflow decisions (one AskUserQuestion call, up to 4 questions)

Phrase them against the real state (substitute the actual branch name). Always
ask the Branch, Commits, and Release-label questions; add the Pull-request
question whenever a PR is plausible (harmless if they pick `No PR`).

1. **header "Branch"** — "Create a new branch off the current branch (`<current>`)?"
   - `New branch` — branch off the current HEAD. You'll propose a name (see §3).
   - `Stay on <current>` — commit on the current branch.
   - If `<current>` is `main`/the default branch, **drop the "Stay" option** —
     never commit to the default branch (CLAUDE.md). Default to a new branch.

2. **header "Commits"** — "How should the changes be committed?"
   - `One commit` — all current changes in a single Conventional Commit.
   - `Split by subject` — group related changes into separate commits.
   - `Amend into existing` — the pending changes are refinements/fixups of
     commits already on the branch; fold each into the commit it belongs to
     rather than adding new ones (see §4 "Amend"). Offer this when the working
     tree looks like tweaks to work you just committed.

3. **header "Pull request"** — branch on whether a PR already exists (from §1):

   - **No PR yet** — "Open a PR after committing?"
     - `Draft PR` — push and open a **draft** PR filled from the template (not
       yet ready for review; CI still runs).
     - `Ready PR` — push and open a PR **ready for review**, filled from the template.
     - `No PR` — stop after committing.
     - Default to whichever fits the user's words; if they just said "ship"/"open
       a PR" with no signal, list `Draft PR` first as the safer default.

   - **PR already exists** (substitute the real number, e.g. `#42`) — "PR `#42`
     is already open — what should I do with it?"
     - `Push updates` — push the new/amended commits to the existing PR; leave the
       description and draft state as-is.
     - `Refine description` — rewrite/update the PR body (and title if it should
       change) from the current state of the branch, then push any new commits.
     - `Toggle draft/ready` — flip the PR's draft state in addition to pushing.
     - `Leave PR alone` — stop after committing; don't touch the open PR.

4. **header "Release"** — "Which release does this cut on merge to `main`?"
   **Always ask when a PR will exist** (new PR, or an existing PR — set/repair the
   label). The PR Checks gate **fails without exactly one** `release:*` label, and
   `release-on-merge.yaml` reads it to pick the version bump:
   - `release:patch` — bug fix / no API change (vX.Y.Z+1).
   - `release:minor` — new feature (vX.Y+1.0).
   - `release:major` — breaking change (vX+1.0.0).
   - `release:skip` — no release for this merge.
   - Recommend the level that matches the diff (feat → minor, fix → patch, a
     breaking CR/flag change → major) and list it first. If the existing PR
     already carries the right label, say so and skip re-applying.

If the diff obviously spans unrelated subjects, you may recommend `Split by
subject` (list it first, label it "(Recommended)"). Otherwise default to one commit.

Note: wsm PRs merge into `main` (no long-lived integration branch), so there is
no "merge into" question — the base is always `main`. Only override it via
`gh pr edit <num> --base <branch>` if the user explicitly asks.

## 3. Branch (if chosen)

Create off the **current** branch (not main — the user asked for current):

```sh
git checkout -b <name>
```

Name follows the repo convention `<gh-username>/<short-desc>`, where
`<gh-username>` is the **current user's actual GitHub login** from `gh api user
--jq .login` in §1 (e.g. `jthakkar04`, `collinol`, `danielpanzella`) — never
hardcode a name. (`fix/...` and `security/...` prefixes also appear in history.)
Keep `<short-desc>` 2–4 kebab words. Propose it; the user can override via the
question's "Other" field if they typed a name.

## 4. Commit(s)

Stage and commit. Every commit message:

- **First line** = Conventional Commit: `type(scope): description`
  - types: `feat fix build chore ci docs perf refactor revert style test`
  - imperative, lowercase, no trailing period, ≤ 72 chars (the gate enforces this).
- **Body** = **at most two sentences** explaining the *why* / non-obvious parts.
  Skip the body entirely for trivial changes. Never pad it.
- **Trailer** (only when Claude authored/co-authored the change): a blank line, then
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

Use a HEREDOC so the body and trailer format cleanly:

```sh
git add <paths>
git commit -m "$(cat <<'EOF'
feat(deploy-v2): add managed-clickhouse flags

The operator's CR now nests ClickHouse under ManagedClickHouseSpec, so surface
the common knobs as typed flags with --cr-set as the escape hatch.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
EOF
)"
```

**Split by subject:** group changed files into coherent sets, state the plan in
one line each, and make one commit per group (each with its own conventional
title + ≤2-sentence body). Stage explicitly per group (`git add <paths>`) so
unrelated files don't leak into the wrong commit. Keep formatting/reformat churn
in its own `style:` or `chore:` commit.

**Amend into an existing commit:** identify which branch commit each pending
change belongs to (`git log --oneline <base>..HEAD`; match by the files it
touched). Then:

- **Tip commit** — stage the change and `git commit --amend --no-edit` (or amend
  the message too if it should change).
- **Earlier commit** — interactive rebase (`git rebase -i`) is **not available**
  in this environment, so rebuild non-interactively: save the pending change as a
  patch, reset to the base, and cherry-pick the commits back, folding the patch
  into the target one:
  ```sh
  git diff -- <paths> > /tmp/fold.patch
  git checkout -- <paths>
  git reset --hard <base>
  git cherry-pick <c1>            # ... up to the target commit
  git apply /tmp/fold.patch && git add <paths> && git commit --amend --no-edit
  git cherry-pick <rest>...        # replay the commits after it
  ```
- **Force-push caution:** amending any commit that's already been pushed rewrites
  history — the later `git push` needs `--force-with-lease`. Call this out and
  treat it as part of the PR / push authorization; never force-push `main` as a
  side effect — that's its own explicit decision.

## 5. PR (if chosen)

### 5.0 Ask: PR presentation (one AskUserQuestion call, 2 questions)

Fire this **second** ask whenever a PR body will be written — i.e. a **new PR**
(`Draft PR`/`Ready PR`) **or** an existing PR you're refining (`Refine
description`). Skip it for `Push updates` / `Toggle draft/ready` / `Leave PR
alone`, which don't rewrite the body. Ask **both** questions in one call:

1. **header "Title"** — "Use this PR title?" Analyze the diff/commits and propose
   a Conventional-Commit title (same rules as §4). List your **recommended title
   verbatim** as the first option, labeled "(Recommended)".
   - **New PR** — offer the recommended title; the user picks it or types their
     own via "Other".
   - **Existing PR** — also offer `Keep current: "<current title>"` as a second
     option when the current title is already valid. Recommend a change only if
     the current title is non-conventional or no longer matches the diff, and say
     why in the option description.

2. **header "Description"** — "How should the Summary read?" This governs the
   prose style of the Summary section only (headings/Jira/checkboxes are fixed by
   the template). Ask this **regardless** of whether a PR already exists:
   - `Bullet points` — a tight bulleted list of what changed and why.
   - `Sentences` — a short prose paragraph.
   - `Mixed` — a lead sentence then bullets for the details.
   - Recommend the fit: multi-subject diffs → `Bullet points` or `Mixed`; a small
     focused change → `Sentences`. List the recommended one first.

Keep the Summary **concise** either way — enough to scan, never a file-by-file
narration. Then proceed to §5a/§5b using the chosen title and style.

### 5a. New PR (`Draft PR` / `Ready PR`)

Push, then open a PR whose body is the **live template, filled — not replaced**.
Add `--draft` for a `Draft PR`; omit it for a `Ready PR`. The base is `main`:

```sh
git push -u origin <branch>
gh pr create [--draft] --base main --title "<conventional title>" --body "$(cat <<'EOF'
...filled template...
EOF
)"
```

Then **apply the release label** chosen in the Release question:

```sh
gh pr edit <num> --add-label <release:level>
```

### 5b. Existing PR (the §2 "PR already exists" answers)

A PR is already open on this branch — **never `gh pr create`** (it errors). Map
the answer to the right `gh` action, and always push the new commits first:

- Push commits: `git push` (or `git push --force-with-lease` if you amended/rebased
  commits that were already pushed — see §4's force-push caution). The open PR
  updates itself.
- `Push updates` — just the push above; leave body and state untouched.
- `Refine description` — rewrite the body from the branch's current state and apply
  it: `gh pr edit <num> --body-file <file>` (and `--title "<conventional title>"`
  if the title should change). Re-read the live template and re-fill it; don't
  diff against the old body blindly. Follow all the body rules below.
- `Toggle draft/ready` — `gh pr ready <num>` to mark a draft ready, or
  `gh pr ready <num> --undo` to send a ready PR back to draft.
- `Leave PR alone` — stop after committing; touch nothing on the PR.

**Release label** — whatever the answer, reconcile the label with the Release
answer: if it's missing or wrong, `gh pr edit <num> --remove-label <old>` then
`--add-label <new>` so exactly one `release:*` label remains (the gate rejects
zero or multiple).

### Body rules (apply to both 5a and any `Refine description`)

- **Read `.github/pull_request_template.md` at runtime** and fill it in place.
  Preserve its exact section headings and the **bolded Jira** line; replace only
  the `<!-- ... -->` guidance comments with real content. The template evolves —
  never hardcode its shape from memory.
- **Title** = the title confirmed in §5.0 (a Conventional Commit, same rules as
  §4). For a single-commit PR the recommendation is that commit's title.
- **Summary** must be real prose ≥ 50 non-whitespace chars (the `PR Body` check
  fails otherwise, excluding the Jira line). Set the Jira key if the user gives
  one; otherwise leave the `ONPREM-XXXX` placeholder for them to edit.
- **Style** — write the Summary in the format chosen in §5.0 (`Bullet points` /
  `Sentences` / `Mixed`). Whichever it is, keep it **concise**: lead with what
  changed and *why*, explain non-obvious decisions in a sentence, and never
  narrate every file. Scannable, not terse enough to lose the overview.
- **Test Plan** — the actual commands/steps used (`make build`, `make lint`,
  `make fmt`, `./wsm list`, or a Kind smoke test for v2). wsm has **no unit-test
  suite** — never invent tests; if you didn't run something, say so.
- **Requirements checkboxes** — check only the boxes that are *genuinely* true.
  The merge gate needs all boxes checked, so if one can't be truthfully checked,
  leave it unchecked and tell the user which and why — don't silently tick it.
- **Footer** — when Claude authored the PR, end the body with a blank line and
  `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- **Never merge.** Open the PR and report its URL.

## Guardrails

- **Run outside the sandbox.** Every command this skill runs — `git` (commit,
  push, `--force-with-lease`), `gh` (pr create/edit/ready, label edits), and
  `make build`/`make lint` for the Test Plan — needs network and the Go build
  cache, both of which the sandbox blocks (`gh` fails TLS verification; `go build`
  fails with "operation not permitted" on its cache). Run these with the sandbox
  disabled.
- Do only what the answers selected (e.g. `No PR` / `Leave PR alone` → stop after
  committing; `Push updates` → push only, don't rewrite the description).
- Pushing, opening a PR, editing its body, toggling draft/ready, and adding the
  release label are all outward-facing — the chosen PR/Release answers are the
  authorization; don't take those steps otherwise.
- Report what you did plainly: branch name, commit subjects, PR URL, whether the
  PR is a draft, and the release label applied. If tests weren't run, say so
  rather than checking the box.
