# Git Sync

Fetch and fast-forward every Git repository in a directory, concurrently.

Install by:
```bash
go install github.com/hxmbl/git-sync@latest
```

Build from source:
```bash
git clone https://github.com/Hxmbl/git-sync/
cd git-sync
go install .
```

`go install` places the `git-sync` executable in your Go bin directory. *Make sure that directory is on your `PATH`*.

# Usage

Run it from the directory containing your projects, or pass that directory:

```bash
git-sync [projects-directory]
```

Repositories are synced concurrently, up to sixteen at a time by default:

```bash
git-sync -j 8 ~/Projects
```

Each repository is fetched exactly once, and only touched afterwards if its
upstream actually moved:

```
Syncing 5 repositories in ~/Projects

•  local-only  skipped (no upstream tracking branch)
✓  beta        up to date
✗  diverged    update failed (diverged)
    ↳ The branch and its upstream have diverged. Rebase or merge manually, then rerun.
✓  gamma       up to date (local ahead by 1 commit)
↑  epsilon     fast-forwarded 1 commit

──────── Summary ───────────────────────────────────────────
  2 up to date  ·  1 fast-forwarded  ·  1 skipped  ·  1 failed
  Finished in 117ms.

  1 repository needs attention:
    diverged    update failed (diverged)

  Retry the failed repositories:
      git-sync --only diverged ~/Projects
```

# Options

```
Usage:
  git-sync [flags] [projects-directory]

Flags:
  Essential:
      -j, --jobs int       repositories to sync at once (default 16)
          --color string   color output: auto, always, or never (default "auto")
      -q, --quiet          only show failures and the summary
      -v, --verbose        show git output and timings for every repository

  Selection:
          --only string    only sync this repository (repeatable)
          --skip string    skip this repository (repeatable)

  Fetching:
          --all-refs       fetch every branch instead of just the upstream
          --tags           also fetch tags (off by default: --no-tags)
          --timeout dur    per-repository network timeout (default 2m0s)

  Output:
          --json           print machine-readable JSON
          --dry-run        fetch and report, but do not modify anything

  Other:
      -h, --help           show this help
      -V, --version        print the version
```

| Status | Meaning |
| --- | --- |
| `↑ fast-forwarded` | the upstream moved and the local branch followed it |
| `✓ up to date` | the local branch is level with its upstream (local commits are reported as `local ahead`) |
| `• skipped` | nothing to do — usually no upstream tracking branch |
| `✗ failed` | the repository needs attention; fetch or update did not succeed |

Colour follows `--color`, then `NO_COLOR`, then `TERM` (`dumb` or empty turns it
off), and only applies when the output is a terminal.

# Exit codes

| Code | Meaning |
| --- | --- |
| 0 | every repository is in the desired state |
| 1 | at least one repository needs attention |
| 2 | bad usage, or the projects directory could not be read |
| 130 | interrupted with Ctrl-C |

# JSON output

`--json` prints one report instead of streaming lines:

```bash
git-sync --json ~/Projects
```

```json
{
  "root": "/home/you/Projects",
  "repositories": [
    {
      "name": "epsilon",
      "status": "fast-forwarded",
      "detail": "fast-forwarded 1 commit",
      "commits": 1,
      "durationMs": 114,
      "remote": "origin",
      "branch": "main"
    }
  ],
  "summary": {
    "total": 1,
    "fastForwarded": 1,
    "durationMs": 117
  }
}
```

# How it works

- **One network round trip per repository.** `git pull` would fetch again
  after an explicit `git fetch`, so `git-sync` fetches once and then
  fast-forwards locally only when the fetched upstream differs from `HEAD`.
- **Repositories that are already current do no update work at all.** `HEAD`
  and the remote-tracking ref are read straight from the repository, so an
  up-to-date repository costs no extra processes after its fetch.
- **Only the checked-out branch is fetched** (`git fetch --no-tags <remote>
  <branch>`), which keeps multi-branch and tag-heavy repositories fast. Use
  `--all-refs` to refresh every branch and `--tags` to include tags.
- **Git's automatic maintenance is disabled during the run** (`maintenance.auto`,
  `gc.auto`), since it would repack after every fetch and merge.
- **Largest repositories start first.** Repositories are ordered by pack size
  before being handed to workers so the slow ones do not own the tail of the
  run, and `--timeout` bounds any single repository that hangs.
- **Nothing blocks on a prompt.** `GIT_TERMINAL_PROMPT=0` is set for every
  command, so a repository needing credentials fails immediately with a hint
  instead of stalling the batch at a hidden prompt.

# Notes

- Credentials must already be available non-interactively (an SSH key in the
  agent, or a credential helper such as `osxkeychain`).
- Diverged and dirty repositories are never merged or rebased automatically —
  they are reported with a suggested next step so you stay in control of
  history rewriting.
- Filters match repository directory names exactly:
  `git-sync --only api --only web ~/Projects`.

## License

MIT — see [LICENSE](LICENSE).
