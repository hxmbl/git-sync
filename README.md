# Git Sync

Install by:
```bash
git clone https://github.com/Hxmbl/git-sync/
cd git-sync
go install .
```

`go install` places the `git-sync` executable in your Go bin directory. Make sure that directory is on your `PATH`.

# Usage

Run it from the directory containing your projects, or pass that directory:

```bash
git-sync [projects-directory]
```

Repositories are fetched and pulled concurrently (up to six at a time by default). Set the concurrency with `-j`:

```bash
git-sync -j 8 ~/Projects
```

Each repository is fetched before it is pulled. If fetching fails, its pull is skipped. Failures include Git's output and a suggested next action; the command exits with a non-zero status if any repository needs attention.

## License

MIT — see [LICENSE](LICENSE).
