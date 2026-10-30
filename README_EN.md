# go-agent-harness (gah)

> English | [简体中文](./README.md) — both files are kept in sync

A programming-agent harness written in Go: one static binary carries TUI / Web / headless / desktop shell, with zero runtime dependencies. The design follows DeepSeek Harness's "everything is a plugin" philosophy — the micro-kernel only owns the ctx service container, the event bus, the plugin registry and the config layer; every capability is a plugin.

- Plugin development: [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md) · Plugin overview: [plugins/README.md](./plugins/README.md)
- Design notes and delivery records are local-only and not distributed; this file plus the commit history are the public delivery overview

## 1. What it is

- **One static binary**: `CGO_ENABLED=0`, six cross-compile targets (darwin / linux / windows on amd64 / arm64); measured 27-29 MiB single file, 12-14 MiB gzipped, gates at 30 / 15 MiB (`scripts/size-check.sh`).
- **Zero runtime deps**: no Node runtime, no node_modules, no version managers; starts under a bare `env -i`.
- **Fully portable**: the data root is `gah-data/` next to the binary, created on first run; `gah` + `gah-data/` is the whole deployment, upgrades replace a single file.
- **One source, three shapes**: TUI, Web (frontend embedded via `go:embed`) and headless are three profiles of the same process; the desktop shell reuses the same sidecar.

## 2. Features

| Capability | Notes |
|---|---|
| **Micro-kernel** | `core/` holds only ctx / event / plugin (topology + hot reload) / config (profile→bundle→patch) / boot — zero business logic |
| **Everything is a plugin** | Sessions, LLM routing, tools, sandbox, approval, the agent loop and the UI are all plugins; `/plugins` toggles them at runtime, unload undoes side effects |
| **Two UIs + headless** | TUI (bubbletea v2) and Web (Vue 3) are feature-equal and share one `$GAH_HOME`; `--profile headless` for CI and scripts |
| **ReAct loop** | `pre-step → llm/stream → tool/call* → turn/end`, configurable step cap (unlimited by default) |
| **In-turn steering** | Pressing Enter mid-turn injects the message into the running turn; falls back to "queued", `Alt+Up` recalls it |
| **Tool pipeline** | MCP-compatible schemas; `pre-execute` (veto-able) → execute → post-execute → result broadcast; structured errors back to the model |
| **Unified LLM model** | OpenAI-compatible adapter (DeepSeek / OpenAI / Ollama / vLLM / Kimi …) + Anthropic adapter + mock; multiple providers side by side |
| **Sandbox, three levels** | read-only / workspace-write / full-access; uniform write-target adjudication incl. shell redirections and output flags; macOS seatbelt and Linux Landlock back it at the process-tree level |
| **Approval, three levels** | open / smart (default, asks and waits) / strict; no confirmation channel means a safe reject |
| **Sessions and context** | Per-project isolation, multiple sessions, fork/clone/tree, rolling summarisation over budget, `/cache` hit-rate diagnostics, `/usage` cross-session usage |
| **Parallel sessions** | Web/desktop tabs run independently; within one session turns stay strictly serialised, single writer per session log |
| **Jobs and schedules** | Background jobs, starlark workflows, subagent fan-out, git worktree isolation, scheduled tasks (Chinese scheduling controls, incl. lunar calendar) |
| **Notice channel** | `ctx.notices` messages aimed at humans (not in the session log, no tokens); TUI uses terminal notifications, the desktop shell uses system notifications |
| **MCP / ACP, both ways** | MCP client bridge and server mode; `gah acp` is a first-class agent for editors, `acp-client` delegates work to external coding agents |
| **Plugin install** | CLI `/install` and the Web panel share one core: source pinning with drift guard, `--prebuilt`, integrity gate, start/stop at will |
| **Desktop shell** | Go + Wails v3: tray, notifications, autostart, single instance, multi-instance, `gah-shell.log` diagnostics; auto-update with its own ed25519 signature |
| **Document preview** | One block model rendered in four places (markdown / text / code / CSV / notebook / PDF / Office): Web workbench, TUI pager, `gah doc` |

## 3. Quick start

### Download

Grab the right file from [Releases](https://github.com/nekoleamo/go-agent-harness/releases/latest):

| Platform | File | Notes |
|---|---|---|
| macOS (Apple silicon) | `gah-<version>-aarch64-apple-darwin.app.zip` (plus `.dmg`) | Unzip and drag gah into Applications |
| Windows (64-bit) | `gah-<version>-setup.exe` | Double click; per-user install, no admin rights |
| Windows portable | `gah-<version>-portable-x86_64-pc-windows-msvc.zip` | No install: unzip into a writable folder and run `gah-desktop.exe` |
| Windows on ARM | `gah-<version>-portable-aarch64-pc-windows-msvc.zip` | Same as above (no separate arm64 installer) |
| CLI (any platform) | `go-agent-harness_<version>_<os>_<arch>.tar.gz` | Single binary, unpack and run (also `.zip` and `checksums.txt`) |

**The OS will block the first launch once** (no Apple / Microsoft signing certificate; the binary itself is fine):

- macOS "unidentified developer" → right-click gah in Applications → Open → Open (once only).
- macOS "is damaged and can't be opened" → the file is not damaged, that is the standard wording for "unsigned + downloaded"; run once: `xattr -dr com.apple.quarantine /Applications/gah.app`.
- Windows SmartScreen → More info → Run anyway.

Then fill in one model provider in the settings panel (one-click presets for DeepSeek / Kimi / Zhipu / Qwen / Ollama).

**Data and upgrades**

- All data (sessions, memory, schedules, keys) lives in `gah-data/`; upgrades and uninstalls leave it alone. Only the uninstaller's "Delete app data" checkbox removes it (unchecked by default).
- The desktop shell takes an extra backup to `~/gah-upgrade-backup/` before upgrading; if that backup fails the upgrade is cancelled.
- Behind a flaky network: the updater tries a Gitee mirror first and falls back to GitHub; a source that serves the manifest but not the artifact is remembered and demoted. Signatures cover file contents only, so re-sourcing does not weaken security.
- **Upgrading a 0.7.x desktop build to 0.8.x needs one manual install**: 0.8.0 swapped the shell from Tauri to Go + Wails v3, which changed the manifest format and signing scheme — the old shell cannot read the new manifest. Install once by hand (data is untouched), then auto-update resumes. The CLI is unaffected.

### Desktop shapes

| | Installed (macOS / Windows) | Portable (Windows only) | CLI |
|---|---|---|---|
| How to run | macOS: drag into Applications; Windows: `-setup.exe` | Unzip into a writable folder, run `gah-desktop.exe` | Unpack the tarball, get `gah` |
| Marker | A shell exists in the app folder | `portable.marker` next to the exe | none |
| Runtime files | Extracted to the user data dir `bin/` (macOS `~/Library/Application Support/dev.gah.desktop/`, Windows `%LOCALAPPDATA%\dev.gah.desktop\`) | Run in place | In the same folder |
| Data | `bin/gah-data/` under that folder | `gah-data/` in the folder | `gah-data/` next to the binary |
| Upgrade | Tray "Check for updates…" → confirm → download → replace → restart | Auto-updates in place; read-only folder falls back to "check only" plus a download hint | Replace the single file |

The installed and portable builds are separate identities and can run side by side; double-launching the same shape just focuses the existing window (use the tray "New instance…" or `--new-instance` to really open another). When something breaks, read `<user data dir>/gah-shell.log` (tray "About gah → Diagnostics" opens it).

### Build

```bash
# Single binary (version taken from the git tag)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o gah ./cmd/gah

# Or the single entry point (guarantees the embedded frontend; VERSION=v0.8.0 ./scripts/gen.sh cli for a release)
./scripts/gen.sh cli
```

### Global install (deploy once, run anywhere)

```bash
# macOS / Linux: build → ~/.local/gah + symlink ~/.local/bin/gah
bash scripts/install.sh

# Windows (PowerShell): build → %LOCALAPPDATA%\gah + add to the user PATH
powershell -ExecutionPolicy Bypass -File scripts\install.ps1
```

Afterwards, from any directory: `gah` / `gah web` / `gah --profile headless -input "…"`. Upgrade = re-run the script, uninstall = `--uninstall`.

> **Windows prerequisite**: the `shell` tool and background jobs run through a POSIX shell, so install [Git for Windows](https://git-scm.com/download/win) (common locations are auto-detected) or set `GAH_SHELL_PATH`. There is no cmd.exe / PowerShell fallback: write-target adjudication is POSIX-lexical, and swapping the shell would decouple the sandbox decision from what actually runs.

### Three run shapes

```bash
./gah                                              # TUI (default; refuses a non-TTY stdin and points at headless)
./gah web                                          # Web UI at http://127.0.0.1:2233, opens the browser
./gah acp                                          # ACP agent shape (editors launch it over stdio)
./gah --profile headless --input "run echo hi"      # one headless turn
./gah --profile mcp-serve                          # MCP server shape (external clients over stdio)
./gah --profile dev --dump-config                  # dump the merged config tree
./gah --ephemeral --profile headless --input "hi"  # throwaway data root
```

### Wiring a real model

```bash
export DEEPSEEK_API_KEY=sk-...     # or OPENAI_API_KEY / ANTHROPIC_API_KEY (for claude-* models)
./gah --profile headless --input "hello"
```

Attach any OpenAI-compatible endpoint from inside the TUI, no config edit and no restart:

```bash
/provider set https://api.siliconflow.cn/v1 sk-<key> deepseek-ai/DeepSeek-V3   # takes effect and persists (0600)
/provider show | use <name> | unset <field> | remove <name> | clear            # inspect / switch / edit / delete / reset
```

### Common CLI flags

| Flag | Purpose |
|---|---|
| `-profile <name>` | profile (tui / headless / dev / web / acp / mcp-serve / custom) |
| `-input <text>` | one headless input, run a turn and exit |
| `-dump-config` | print the merged config tree |
| `-ephemeral` | throwaway data root, gone on exit |
| `-version` | version info (pure query, fine in pipes and CI) |
| `-install <repo\|local dir>[@version or commit]` | install a plugin; `mcp:<id>:<command>` registers an MCP plugin |
| `-prebuilt` / `-accept-drift` | install the author's prebuilt artifact / accept a tag that moved to a new commit |
| `-install-artifact <url> -id <id> -name <tool-x>` | install an artifact someone else built (no toolchain needed) |
| `-check-plugin-updates` | `git ls-remote` only, no clone and no install |
| `-list-plugins` / `-uninstall <id>` | list external plugins (with repo/ref/commit) / uninstall |
| `-trust-plugin` / `-untrust-plugin` / `-list-trusted-plugins` | hash allow-list: add / revoke / list |
| `-install-ui` / `-list-ui-plugins` / `-uninstall-ui` / `-trust-ui-plugin` | UI plugin install and the integrity-gate escape hatch |

```bash
gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--tree] [--depth N]
# exit codes: 0 ok / 2 usage / 3 unsupported format / 4 over budget / 5 parse failure
# --convert: use a local LibreOffice to turn legacy Office files into PDF first (off by default)
```

## 4. TUI commands

Commands register into `ctx.commands` and work in TUI / Web / headless behind the `/` prefix; typing `/` shows completions.

| Command | Purpose |
|---|---|
| `/model <name>` | switch model (enumerated from the current endpoint; a role-declared model only changes the session slot and says so) |
| `/thinking off\|low\|medium\|high` | reasoning level (`Shift+Tab` cycles) |
| `/sandbox ro\|ws\|full` · `/sandbox sync on\|off` | sandbox level / approval-to-sandbox coupling switch |
| `/approval open\|smart\|strict` | approval level (persisted) |
| `/provider show\|add\|use\|set\|unset\|remove\|clear` | manage multiple providers |
| `/plugins list\|on\|off <id>` | plug and unplug plugins at runtime |
| `/compact [hint]` · `/cache [n]` · `/context [all]` · `/usage [days\|all]` | manual compaction / cache diagnostics / context breakdown / cross-session usage |
| `/workspace [dir]` · `/session list\|switch\|new\|current\|pin\|summary` | workspace and session management |
| `/fork [seq]` · `/clone` · `/tree` · `/name <name>` | branch, clone, view the tree, rename |
| `/role list\|use\|new\|rm\|export\|import` · `/role-pack export\|import` | roles and role packs |
| `/skill-export` · `/skill-import` | skill packs |
| `/diff [path]` · `/recap` · `/traj` · `/plan` · `/todo` | change review / recap / trajectory / plan / todos |
| `/jobs` · `/worktree` · `/schedule` | background jobs / isolated worktrees / schedules |
| `/backup [dest]\|list\|restore <name>` | whole-home backup and restore |
| `/preview <path>` · `/search <word>` · `/theme [name]` | document preview / in-session search / theme |
| `/export [path]` | export the session (`.html` = self-contained page, otherwise jsonl; Web/desktop equivalent: sidebar ⤓ and the top-bar "Export") |
| `/reload` · `/settings history N` · `/statusline` · `/notify` · `/help` | hot reload / history depth / status line / system notifications / help |

> A leading `!` runs a shell command through the same sandbox and approval pipeline as model tools. To quit, press `Ctrl+C` twice.

## 5. Model-facing tools

| Tool | Notes |
|---|---|
| `shell` | run commands (sandbox and approval apply, write targets adjudicated, credential env stripped, environment jail; `data.pty` drives interactive processes) |
| `file_read` / `file_write` / `file_append` / `file_edit` | read and edit; `file_read` pages via `offset` / `limit` and reports truncation honestly |
| `file_list` / `file_glob` / `file_grep` | list / glob / content search |
| `file_mkdir` / `file_move` / `file_copy` / `file_rename` | never overwrite by default; temp file plus rename for atomic writes, copies across devices |
| `file_delete` / `file_trash_list` / `file_restore` | trash by default (`$GAH_HOME/trash/`); `permanent: true` really deletes |
| `image_ops` | info / convert / resize / crop / rotate / compress / thumbnail (metadata incl. GPS stripped by default) |
| `web_fetch` / `web_search` | fetch a page / search the web (AnySearch by default, no key; anti-injection and private-address blocks on by default) |
| `workflow` / `workflow_collect` | starlark scripts composing tool calls (sandboxed, can run in background) |
| `job_list` / `job_output` / `job_kill` | background jobs |
| `memory` · `todo` · `auto_plan` · `session_search` | cross-session memory / todos / plan mode / past-session search |
| `schedule` | schedule management (disabled by default, enable in the config layer) |
| `subagent` | subagent delegation; `isolate="worktree"` runs it in a managed git worktree |
| `list_skills` / `read_skill` · `list_roles` / `read_role` | skills and roles (roles are read-only; writes go through `/role` and the panel) |
| `mcp_<server>_<tool>` · `mcp_search` / `mcp_call` | MCP tools (direct mode / on-demand search mode) |
| `ask_user_question` | structured questions (Web overlay, `/answer` in the TUI) |

## 6. Web

`gah web` serves `http://127.0.0.1:2233` (`GAH_WEB_OPEN=0` disables the browser launch). Feature-equal to the TUI, plus visual panels: settings (model / reasoning / instructions / roles / history / provider / backup / schedules / MCP / plugins), a document-preview workbench, trajectory / changes / board views, sidebar docking, structured questions, attachments and image input.

In `token` mode (non-empty `data.auth_token`) every surface requires auth; `POST /api/auth` is the only exempt path.

Main REST surface (unwired services answer 503/501):

| Path | Purpose |
|---|---|
| `GET /api/state` · `POST /api/input` · `POST /api/confirm` · `POST /api/control` | state snapshot (`?session=`) / submit a turn (steers while running) / approval reply / model, sandbox, approval, cancel |
| `GET /api/events` · `GET /api/events/ws` · `GET /api/session/events` | event stream (SSE / WS, resumable by `after`) plus paging for long sessions |
| `GET/POST /api/sessions` · `rename` · `/api/sessions/{id}/export` | session list and switch/new/spawn/fork/clone/delete / rename / export |
| `GET/POST/DELETE /api/providers` · `GET /api/models?all=1` | provider CRUD and model aggregation |
| `GET /api/tools` · `POST /api/tools/{name}` · `GET /api/commands` | direct tool and command calls |
| `GET/POST /api/schedules` (`PATCH` / `DELETE` / `run` / `resolve`) | schedules; `resolve` turns Chinese text or cron into a schedule |
| `GET/POST /api/mcp` · `GET /api/plugins` · `POST /api/plugins/{id}/load\|unload` | MCP config (write plus hot reload) / plugin toggling |
| `POST /api/attachments` + `GET /attachments/...` | attachment upload (20MB, type allow-list) / preview |
| `GET /api/jobs` · `GET /api/todo` · `GET/POST /api/backup` | jobs / todos / backup and restore |
| `POST /api/compact` · `POST /api/reload` · `POST /api/shutdown` | compaction / instruction hot reload / graceful shutdown |
| `GET /api/doc/preview\|raw\|asset\|tree\|html` · `POST /api/doc/render` | document preview (block model / raw bytes / assets / tree / HTML sandbox) |

## 7. Config and runtime layout

### Config layers (profile → bundle → patch)

```
profile-<name>.yaml     # bundles + patches, in order
bundle-base.yaml        # plugin entries (id + enabled + data)
patch-*.yaml            # replace / insert / toggle entries by id
```

Sample sources live in `config/` (kept in sync with `internal/embed/seed`, header `# seed-version: N`). An entry id is `<type>-<name>`, matching its `plugins/` directory.

### Data root (fully portable)

- The data root is **`gah-data/` next to the binary** (the only one; created on first run together with the config samples and bundled plugins). If it cannot be created (read-only folder) startup fails loudly — move to a writable folder.
- Keys live in `gah-data/config/` (provider.yaml / search.yaml) and travel with the directory.
- Whole-home backup: `/backup` packs all of `gah-data/` (minus `backups/` itself) into `gah-data/backups/` or an external path.

### Environment variables (common)

| Variable | Purpose |
|---|---|
| `GAH_PROFILE` / `GAH_NO_TUI` | default profile / force the TUI off |
| `GAH_WEB_ADDR` / `GAH_WEB_OPEN` / `GAH_WEB_STATIC` / `GAH_WEB_TOKEN` | Web listen address / auto-open browser / static dir override (HMR) / desktop shell token |
| `GAH_EXPORT_OPEN` | open `/export` output automatically (`0` disables) |
| `GAH_MCP_COMMAND(S)` | MCP bridge wiring (`config/mcp.yaml` wins) |
| `GAH_SHELL_KERNEL_SANDBOX` / `GAH_SHELL_CRED_READ_KERNEL` / `GAH_SHELL_JAIL` | shell kernel sandbox / credential-read denial / environment jail (all on by default, `0` disables) |
| `GAH_EXT_ENV_PASS` · `GAH_EXT_KERNEL_SANDBOX` · `GAH_EXT_RW_PATHS` | external plugins: env pass-list / kernel sandbox / extra writable paths |
| `GAH_WEB_APPROVE` · `GAH_WEB_ALLOW_HOSTS` · `GAH_WEB_ALLOW_PRIVATE` | `web_fetch` egress approval / static allow-list / allow private addresses |
| `GAH_SEARCH_PROVIDER` / `GAH_SEARCH_API_KEY` / `ANYSEARCH_API_KEY` / `EXA_API_KEY` | search provider and keys (env wins over `search.yaml`) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM keys (or write them via `/provider`) |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` | egress proxy; the desktop shell falls back to the macOS system proxy |

> `GAH_HOME` is an internal plumbing variable (boot sets it to the portable root); a user-supplied value is ignored.

### MCP

```yaml
# $GAH_HOME/config/mcp.yaml (written by the settings panel's "MCP server" section; hot reloads on save)
servers:
  - name: codegraph
    command: codegraph serve --mcp
    mode: search        # search: expose only mcp_search / mcp_call to the model, saving context
  - name: remote-crm
    transport: http
    url: https://mcp.example.com/rpc   # https required; plain http only for loopback
```

MCP servers are third-party code, so they are launched inside the kernel sandbox. To expose this repo's tools as an MCP server: `./gah --profile mcp-serve`.

### ACP

`gah acp` lets editors drive gah as an ACP v1 agent (sessions, streaming, tool progress and approvals all over ACP, sharing the same session ledger as TUI/Web):

```jsonc
// Zed (~/.config/zed/settings.json)
{ "agent_servers": { "gah": { "command": "/path/to/gah", "args": ["acp"] } } }
```

One process serves one workspace and runs one turn at a time; the editor's permission buttons map to `allow_once` / `reject_once` only.

### Plugin install

```bash
./gah -install <repo>[@version or 40-char sha]        # external plugin: clone → build → $GAH_HOME/plugins/<id>/ → register
./gah -install <local dir>                            # local plugins need no git init + push first
./gah -install-ui <repo|local dir>                    # UI plugin (two refusals: v-html scan, bare process.env)
./gah -install-artifact <url> -id <id> -name <tool-x> # install a prebuilt artifact, no toolchain needed
```

- **Source pinning**: a tag that now points at a new commit is refused by default (`-accept-drift` opts in); `@<sha>` reinstalls that exact commit.
- **Check, don't auto-update**: `-check-plugin-updates` only runs `git ls-remote`; without signatures there is no auto-update channel.
- **Integrity and lifecycle**: `SHA256SUMS` is enforced once it exists (UI plugins too); disable is not uninstall, and a disabled plugin stays disabled across restarts.
- **Build containment**: `-mod=readonly` builds, credential env scrubbing, semantic flags such as `GOFLAGS` stripped; `--prebuilt` runs no repo script at all.

### Instructions and skills

- **Instructions**: global `$GAH_HOME/AGENTS.md` plus project `AGENTS.md` (nearer wins, `AGENTS.override.md` replaces at the same level), injected automatically, `/reload` hot-reloads.
- **Skills**: project `.gah/skills/`, `$GAH_HOME/skills/` and the current role's private skills; the model loads them on demand via `list_skills` / `read_skill`.
- **Roles**: `$GAH_HOME/roles/<id>/` (persona, working rules, private skills, optional model / thinking level, tool exclusions, tighten-only permission levels); 12 preset roles in five groups; `/role` or the panel switches instantly without changing sessions.
- **Themes**: `$GAH_HOME/config/themes/*.yaml`, switched at runtime with `/theme`.

## 8. Plugin development

Read [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md) (also registered as the `gah-plugin-dev` skill, so the model can load it):

1. `plugins/<type>-<name>/` implements `sdk.Plugin` (Start registers side effects, returns a Disposer)
2. Register in `plugins/catalogue` (provides / requires / bundle)
3. Add an entry to `config/bundle-base.yaml` and mirror the sample into `internal/embed/seed`, bumping `seed-version`
4. Unit tests green under `-race`, then commit

Hard rules: plugins import `sdk/` only — never `core/`, `tui/` or another plugin; registration is a side effect and unload must undo it; disposers are idempotent.

## 9. Layout

```
├── cmd/gah/      # boot: profile assembly + CLI flags
├── core/         # micro-kernel: ctx / event / plugin / config / boot
├── sdk/          # the only interface surface plugins depend on (separate module)
├── bundles/      # bundle assembly (base / tui / web / register)
├── plugins/      # host / adapter / policy / tool / mcp / ui plugins (catalogue is the source of truth)
├── extplugins/   # external-process plugin entry points
├── tui/          # bubbletea v2 UI
├── web/          # Web server (SSE/WS + REST + static hosting)
├── web-src/      # Vue 3 + Vite + TS frontend (build output is embedded)
├── desktop/      # Go + Wails v3 desktop shell
├── tests/        # end-to-end and unload-matrix tests
└── scripts/      # build / release / gates / checks
```

## 10. Build and release

```bash
./scripts/gen.sh cli       # single binary for this platform (TUI + Web + headless, frontend embedded)
./scripts/gen.sh release   # goreleaser, six targets (tar.gz / zip + checksums)
./scripts/gen.sh desktop   # desktop shell: sidecar → shell build → .app / .app.zip (optional dmg)
./scripts/gen.sh web-dev   # frontend HMR (vite watch + GAH_WEB_STATIC)
```

- **Gates (CI and local share one source)**: `bash scripts/gate.sh` (gofmt + vet + staticcheck, three modules), full `go test -race`, `scripts/coverage-check.sh` (per-package ratchet plus a global floor), `scripts/size-check.sh` (single file ≤30 MiB / gz ≤15 MiB), frontend `npm test` and `npm run test:layout` (real-browser layout regression).
- **Release**: pushing a `v*` tag triggers two pipelines (CLI and desktop shell) that share one GitHub Release and `desktop-latest.json`; the Gitee mirror is synced locally by `scripts/mirror-gitee.sh`.
- `sdk/` is a separate module (`go.work`), so `go vet/test/staticcheck` must run inside it too.

## 11. License

**MIT License** (see [LICENSE](./LICENSE), © 2026 nekoleamo): use it, modify it, ship it closed-source commercially.
