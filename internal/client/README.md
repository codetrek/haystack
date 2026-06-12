# client

Package `client` is Haystack's **CLI front end**. When the `haystack` binary is
not launched in daemon mode, `cmd/haystack/main.go` dispatches to `client.Run()`,
which parses the subcommand and forwards the request to a running server over
HTTP. It is a thin presentation layer: it builds request structs, posts them to
the server's `/api/v1/...` endpoints, and renders the responses for the
terminal.

The one exception is `server run`, which starts the daemon in-process rather than
talking to a remote one.

## Responsibility / scope

- Parse the top-level subcommand and per-command flags (`Run`,
  `processCommand`, `PrintUsage`).
- Marshal requests, call the server, and format results for each command:
  `search`, `files`, `symbols`, `workspace`, `server`, `version`, `help`.
- Choose the transport (Unix socket when configured, otherwise loopback TCP).

## Transport (`common.go`)

`serverRequest(api, postData)` is the shared HTTP entry point used by every
remote command. It POSTs JSON to `http://…/api/v1<api>` with a 30 s timeout and
decodes the standard `types.CommonResponse` (`Code`/`Message`/`Data`) envelope,
returning an error when `Code != 0`. Transport selection (matching
`docs/architecture.md`):

- If `Global.SocketPath` is set, it dials that **Unix socket** (URL host
  `unixsocket`).
- Otherwise it uses **loopback TCP** at `127.0.0.1:<Global.Port>`.

## Commands

| Command | File | Behavior |
|---------|------|----------|
| `search <query>` | `handle_search.go` | Content search. Flags: `-limit`, `-limit-per-file`, `-path`, `-include`, `-exclude`, `-workspace`, `-case-sensitive`, `-whole-word`. POSTs `/search/content`, prints matched lines with positions. |
| `files <query>` | `handle_search.go` | Filename fuzzy search. Flags: `-limit`, `-workspace`. POSTs `/search/files`. |
| `symbols <query>` | `handle_symbols.go` | Symbol search. Flags: `-limit`, `-limit-per-file`, `-workspace`, `-fuzzy`. POSTs `/search/symbols`. |
| `workspace <sub>` | `handle_workspace.go` | `list`, `get`, `create`, `delete`, `sync`, `sync-all`, `move` over the `/workspace/*` endpoints. |
| `server <sub>` | `handle_server.go` | `status`, `start`, `stop`, `restart`, `run [-d]`. |
| `version` | `client.go` | Prints `running.Version()`. |
| `help [command]` | `client.go` | Top-level usage, or re-dispatches `<command> -h`. |

Defaults for workspace and limits come from `internal/conf`
(`Client.DefaultWorkspace`, `Client.DefaultLimit`).

## Files

- `client.go` — `Run`, command dispatch (`processCommand`), and `PrintUsage`.
- `common.go` — `serverRequest`, the Unix-socket-else-TCP HTTP client.
- `handle_search.go` — `search` and `files` commands plus result rendering.
- `handle_symbols.go` — `symbols` command.
- `handle_workspace.go` — workspace subcommands and `printWorkspace`.
- `handle_server.go` — server subcommands. `server start`/`restart` may launch a
  new daemon (`running.StartNewServer`); `server run` starts the daemon in the
  current process via `server.Run()` (with `-d` delegating to
  `running.StartNewServer`).
- `help.go` — `wantsHelp`, the shared `-h` / `--help` check.

## Relationships

- **Invoked by** `cmd/haystack` (CLI mode).
- **Talks to** `internal/server/httpapi` over HTTP (TCP or Unix socket) for all
  remote subcommands.
- **Imports** `internal/server` only for `server run` (in-process daemon start);
  all other commands go through the HTTP API.
- **Consumes** `internal/conf` (transport config, defaults),
  `internal/shared/running` (server lifecycle helpers, version, executable name),
  and `internal/shared/types` (request/response shapes shared with the server).
