# hive-cli

Agent-facing command line for the hive library. Every command prints one JSON
document to stdout, so LLM agents and scripts can consume the output directly.
It opens the same SQLite database as the desktop app (WAL journaling and a
busy timeout make concurrent use safe), and any read/starred change is queued
for the next GReader sync when a server is linked.

Build: `task build:cli` (or `go build -o bin/hive-cli ./cmd/hive-cli`).

```
hive-cli [-data DIR] <command> [args]

home                        Library overview: counts + latest unread titles
feeds                       All subscriptions (id, title, folder, unread)
list [-feed ID|-unread|-starred|-later|-tag ID] [-limit N] [-offset N]
search <query> [-limit N]   Full-text search over titles and bodies
read <articleID> [-mark]    Full article (plain-text body); -mark marks read
mark <articleID> -read=t|f -star=t|f -later=t|f
tags                        All tags
subscribe <url> [-folder NAME]   Direct feed URL, HTML page, or rsshub://
refresh [-feed ID] [-folder ID]  Fetch feeds now (all, one, or a folder)
```

`-data DIR` overrides the data directory (useful for tests and sandboxes);
the default is the desktop app's own (`~/Library/Application Support/Hive` on
macOS). Subscribe intentionally skips the GUI's YouTube/Reddit page-resolution
specials — use the feed URL directly for those.
