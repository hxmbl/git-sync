# Git Sync

Fetch and fast-forward every Git repository in a directory, concurrently.

Install by:
```bash
go install github.com/hxmbl/git-sync/cmd/git-sync@latest
```

Build from source:
```bash
git clone https://github.com/Hxmbl/git-sync/
cd git-sync
go install ./cmd/git-sync
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

A live progress line tracks the run. Successful repositories stay silent;
only ones that need attention are listed as they finish:

```
!  diverged   has diverged from upstream
    ↳ Rebase or merge manually, then rerun.
!  dirty      has uncommitted changes
    ↳ Commit or stash your local changes, then rerun.
!  ahead      needs push (1 commit ahead)
    ↳ Run 'git push' when you're ready.
Syncing ⠋ 4/5
```

When everything is clean:

```
✓ All good · 5 synced successfully
  Finished in 117ms.
```

# Options

```
Usage:
  git-sync [flags] [projects-directory]

Flags:
  Essential:
      -j, --jobs int       repositories to sync at once (default 16)
          --color string   color output: auto, always, or never (default "auto")
      -v, --verbose        show every repository, plus git output on failures

  Selection:
          --only string    only sync this repository (repeatable)
          --skip string    skip this repository (repeatable)

  Fetching:
          --all-refs       fetch every branch instead of just the upstream
          --tags           also fetch tags (off by default: --no-tags)
          --timeout dur    per-repository network timeout (default 2m0s)

  Output:
          --json           print machine-readable JSON (for scripts)
          --dry-run        fetch and report, but do not modify anything

  Other:
      -h, --help           show this help
      -V, --version        print the version
```

| Attention | Meaning |
| --- | --- |
| `has uncommitted changes` | local edits block a fast-forward |
| `has diverged from upstream` | local and remote each have unique commits |
| `needs push` | local commits have not been pushed |
| `has no upstream` | no tracking branch configured |
| `fetch failed` | network, auth, or remote error |

Colour follows `--color`, then `NO_COLOR`, then `TERM` (`dumb` or empty turns it
off), and only applies when the output is a terminal. The spinner only animates
on a TTY.

# Exit codes

| Code | Meaning |
| --- | --- |
| 0 | sync finished; any remaining attention lines are informational (ahead / no upstream) |
| 1 | at least one repository failed to sync (dirty, diverged, fetch error, …) |
| 2 | bad usage, or the projects directory could not be read |
| 130 | interrupted with Ctrl-C |

# JSON output

`--json` prints one report instead of the progress UI — handy for scripts and CI:

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

## Project layout

- `cmd/git-sync` contains the executable entry point.
- `internal/app` handles command-line options and run orchestration.
- `internal/syncer` discovers and synchronizes repositories.
- `internal/git` wraps Git commands and reads repository refs.
- `internal/report` formats terminal and JSON output.
- `internal/model` defines data shared between the internal packages.
