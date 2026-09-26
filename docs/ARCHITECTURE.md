# Architecture

bnd is a command-line client for Baidu Netdisk, for people and AI agents. This
document is the map: what lives where, and the few rules that keep it small.

## Layers

```
main.go              → cli.Main(os.Args)
internal/cli         commands, flags, output (human / --json), exit codes, shell
   │
   ├── internal/transfer  moving file data between disk and netdisk: parallel
   │                      ranged download, chunked upload, resume records
   ├── internal/config    settings and accounts on disk
   ├── internal/browser   cookie import from Chrome / Edge
   └── internal/update    self-update from GitHub releases
   │
internal/baidu       the Baidu API: one method per endpoint, typed results,
                     typed errors. No printing, no files, no globals.
```

Dependencies point down only. `baidu` knows HTTP and Baidu's protocol;
`transfer` knows files and bytes and uses `baidu` for links and upload
sessions (its ranged downloader itself is generic); `cli` wires them together
and is the only package that prints. The protocol itself is written up in
[baidu-api.md](baidu-api.md).

## `internal/baidu`

A `Client` holds an `*http.Client` and the login cookies. Every endpoint is a
method that builds a request, sends it through `Client.do`, and decodes the
response into a typed struct. `do` owns the cross-cutting parts: headers, the
two error conventions (PCS `error_code` and pan `errno`), and retry of
transient failures.

Errors are `*baidu.Error{Op, Code, Message}`. Callers classify with
`errors.Is(err, baidu.ErrNotFound)` and friends; the numeric `Code` is kept for
`--json`.

## `internal/transfer`

- `Download` fetches one remote file with N ranged connections into
  `name.bnd-part`, records finished ranges beside it, and renames on success.
  An interrupted download resumes from the record. Expired links are refreshed
  through a callback, so the engine does not know about Baidu.
- `Upload` hashes a local file, tries an instant upload, otherwise uploads
  blocks concurrently and commits them. The upload id and finished blocks are
  saved so a rerun continues.
- Both report progress as events; the CLI renders them.

## `internal/cli`

Commands are structs parsed by [kong](https://github.com/alecthomas/kong).
Each command has one method:

```go
func (c *lsCmd) Run(app *App) (Result, error)
```

It returns a result and an error, never prints its result itself. `App.run`
turns that into output:

- human: `Result.Human(w)` writes the text form to stdout;
- `--json`: one document on stdout, `{"ok", "command", "error"?, ...result}`;
- progress and logs always go to stderr.

A command that fails part-way returns both the partial result and the error,
so the JSON still says what was done.

Errors map to exit codes in one place (`exit.go`):

| code | kind | cause |
|---|---|---|
| 0 | – | done |
| 1 | failed | server, network, transfer |
| 2 | input | not found, exists, no wildcard match, bad link or code |
| 3 | dependency | missing tool or permission for browser login |
| 4 | auth | not logged in, login expired |
| 64 | usage | bad command line; interaction needed without a terminal |
| 130 | cancelled | interrupted |

## Libraries

| need | library | license |
|---|---|---|
| command line | alecthomas/kong | MIT |
| browser cookies | browserutils/kooky | MIT |
| retry with backoff | cenkalti/backoff | MIT |
| rate limit, errgroup | golang.org/x/time, golang.org/x/sync | BSD |
| progress bars | schollz/progressbar | MIT |
| interactive shell | chzyer/readline | MIT |
| record / replay in tests | dnaeon/go-vcr | BSD-2 |

## Testing

- `internal/baidu`: request building and response decoding against recorded
  responses.
- `internal/transfer`: local `httptest` servers and fake upload targets.
- `e2e`: runs the built binary through scenarios. With `-record` it talks to
  Baidu with a real account (only under `/bnd-test`) and saves the HTTP
  exchanges; by default it replays them offline and compares stdout, stderr
  and exit codes with the recorded run.
