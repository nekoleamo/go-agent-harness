# go-agent-harness (gah)

A programming-agent harness implemented in Go, shipping **all capabilities as a single static binary** with zero runtime dependencies. It follows the "everything is a plugin" philosophy of DeepSeek Harness and Cordis: the microkernel only handles plugin load/unload and dependency management (no agent logic, no UI). Every capability lives in a plugin that the config layer (`profile → bundle → patch`) can switch on or off at will.

> English edition of [README.md](./README.md). Keep both in sync when updating. 中文版见 [README.md](./README.md)。
> Design references: [DeepSeek Harness](https://github.com/deepseek-ai/DeepSeek-Harness) (TS/Cordis), [naamfung/dsc](https://github.com/naamfung/dsc) (Go/go-plugin/gRPC), [pi coding agent](https://www.npmjs.com/package/@earendil-works/pi-coding-agent) (TS terminal harness).
> - Full design: [DESIGN.md](./DESIGN.md) (§14.1 delivery table / backlog)
> - Plugin development: [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md); plugin overview: [plugins/README.md](./plugins/README.md)

---

## 1. Positioning

**Removes the Node.js dependency that dsh carries**:

- Single static binary (`CGO_ENABLED=0`, **41–46 MiB** across five targets; compressed download ≈27–31 MiB; external plugins are embedded gzip, gate in `scripts/size-check.sh`): no node runtime, no node_modules distribution chain, no version manager;
- `scp` one file to any machine and it works; runtime dependencies = 0 (boots fine under `env -i`);
- Six-target cross-compilation (darwin/linux/windows × amd64/arm64);
- Portable data root: `gah-data/` is auto-created next to the binary; the deployment directory (gah + gah-data) is self-contained; upgrades replace the single binary only.

One source, three surfaces: the same gah binary hosts **TUI / Web / headless**; the Web frontend is embedded via `go:embed` (zero build at runtime).

## 2. Feature highlights

| Capability | Description |
|---|---|
| **Microkernel** | `core/` holds only the ctx service container / 5-dispatch EventBus / plugin registry (topological + hot-reload) / config layer (profile→bundle→patch); zero business logic |
| **Everything is a plugin** | Session log, LLM routing, tool pipeline, sandbox, approval, agent loop, UI — all plugins; the config layer and runtime `/plugins` toggle them at will; unloading revokes side effects |
| **Dual UI** | TUI (bubbletea v2) and Web (Vue3, auto-opens the browser) are feature-equivalent over the same `$GAH_HOME`; plus a UI-less one-shot headless form (CI/scripts) |
| **ReAct loop** | dsh-style turns: pre-step → llm/stream → tool/call* → turn/end; AgentLoop itself is replaceable |
| **Structured tools** | MCP-compatible JSON schema; execution pipeline pre-execute(veto) → execute → post-execute → result broadcast; structured errors fed back to the model |
| **Unified LLM model** | Pure HTTP+SSE OpenAI-compatible adapter (DeepSeek/OpenAI/Ollama/vLLM/Kimi/llama.cpp) + Anthropic adapter (`claude-*` prefix routing) + mock adapter (CI without network); multiple providers coexist (`/provider`) |
| **Sandbox tiers** | read-only / workspace-write (`../` traversal blocked) / full-access; switch at runtime via TUI `/sandbox` or the Web settings panel ; **unified write-path adjudication**: `file_*` arguments *and* the write targets of `shell` commands (redirections, write commands, output flags such as `-o/-O/-C/-t/--target/--prefix`, `git clone` destination) must stay inside the mode's allowed scope (out-of-scope writes and unresolvable targets such as variable-built paths are rejected) — approving a prompt does **not** widen the mode; switch to full-access explicitly. **Tier linkage is visible**: approval `open`/`strict` overrides the *effective* sandbox tier (`full-access`/`read-only`), and `/sandbox`, `/approval`, the TUI status bar and the Web state all echo «declared → effective (source of the override)» instead of failing silently . **Kernel-level sandbox (group 3)**: macOS seatbelt / Linux Landlock backstop writes whose *target cannot be adjudicated* (interpreter-internal writes, `ccache`/`make` wrappers, `go install`, `curl -O`, ...), and the **write-target table** keeps growing (group 2: `sort -o`, `patch -o/-d`, `cargo --target-dir`, `npm --cache`, `pip --cache-dir/-d`, `gcc -MF/-MJ`, `go test -coverprofile/-trace`, `find -exec/-delete/-fprint`, `mktemp -p`, `split`, `tar czf`, `zip`/`7z`, `cmake --prefix`); **Windows still has the cooperative layer only** (no equivalent unprivileged mechanism) |
| **Approval tiers** | Dangerous commands (rm -rf / git push -f / sudo / chmod 777…) follow a tier: open (allow) / smart (confirm dialog; safe-deny when no confirm channel; default) / strict (deny); preference persists |
| **Credential isolation** | Tool subprocess env strips `*_API_KEY/_TOKEN/_SECRET`; dangerous operations safe-deny without a confirm channel |
| **Session management** | Per-project isolation + multi-session switching + branch tree (`/fork` `/clone` `/tree` + naming); rolling summary compression over token budget (token-compress; full logs stay on disk) |
| **Model tools** | shell / file_read-write-append-edit / web_fetch / web_search / workflow / subagent / todo / memory / auto_plan / job_* / skill reader / MCP bridge tools (see §5) |
| **Background jobs** | host-jobs + workflow `background`: async submit/fetch/kill long tasks without blocking a turn |
| **Scheduled tasks** | host-schedule (`ctx.schedule`): 5-field cron plans (min hour dom month dow) stored in `$GAH_HOME/schedules/*.yaml`; when due they run a turn **through the existing turn entry** (agentLoop to tools, still subject to approval/sandbox policy and still written to the session log); managed in the settings panel "Plans" section (Chinese "next run time" read-back, no self-built cron builder); **unattended = no confirmation channel, so actions needing approval are always refused (including in open mode)** |
| **Subagent fanout** | `agent/parallel/pipeline` ReAct fan-out in isolated contexts, results aggregated; `send_message`/`fork` inject messages and derive sessions |
| **starlark workflow** | Model writes a restricted starlark script composing multi-step tool calls (sandboxed, no stdlib); `background` for async |
| **Web search** | web_search (default Exa, `EXA_API_KEY`; switch via `data.provider`) works with web_fetch; errors normalized |
| **MCP both ways** | client bridge (attach external servers; tools `mcp_<server>_<name>`) and server side (expose all repository tools; launchable by Claude Desktop etc.) |
| **MCP on-demand search** | per-server `mode`: `direct` (default, full tool set in context) or `search` (tools stay **out of the per-turn context**; only `mcp_search` to look them up and `mcp_call` to invoke). Config lives in `$GAH_HOME/config/mcp.yaml` (add/edit/remove in the settings panel's "MCP server" section; **saving writes the file and hot-reloads the plugin**, no restart). Env vars still work (file wins) |
| **External plugin bridge** | host-bridge: out-of-process plugins (go-plugin); crash isolation (host survives a killed child); callback channel (GAH_CB_ADDR) so external processes can request tools/jobs/fanout; tools are 100% external (extplugins/) |
| **Plugin install** | `gah -install <repo>[@version]` (external/bridge plugins) and `-install-ui <repo|dir>` (UI-slot plugins): one command, enabled immediately |
| **Instruction files & skills** | Global/project AGENTS.md auto-injected (closer overrides; `/reload` hot reload); SKILL.md scanning with on-demand loading (`list_skills`/`read_skill`); ships the `gah-plugin-dev` skill |
| **Externalized themes** | `$GAH_HOME/config/themes/*.yaml` + `/theme` runtime switching — reskin without recompiling |
| **Full backup/restore** | `/backup` packs GAH_HOME (config incl. keys/plugins/sessions/env.sh/prefs) into a deterministic tar.gz; `list|restore`; restore auto-backs-up the current state first |
| **Config self-healing** | On boot failure, automatically rolls back to the last good backup and retries once; broken config does not brick startup |
| **pty interaction** | tool-shell `data.pty` flag: drive REPLs, git editors, and other interactive processes |
| **Web attachments + multimodal** | Drop images/files into the input (button + drag-drop + paste), chip preview/remove; images injected structurally through openai/anthropic adapters (the model sees images); text files referenced by path; stored under `$GAH_HOME/attachments/` |
| **Document preview** | One block model rendered by four front ends (markdown/text/code/CSV/notebook + PDF page facts): Web preview workbench (file tree / native PDF viewer / HTML source view + sandbox), TUI `/preview` pager (scroll/search/horizontal), `gah doc` CLI, and markdown rendering in the web session stream; every path goes through the sandbox + escape checks + secret deny-list, zero `v-html` |
| **Graceful shutdown** | `POST /api/shutdown` → DisposeAll full teardown (the cross-platform stop channel, incl. Windows without SIGTERM; reused by the desktop shell) |
| **Desktop shell (P1)** | `desktop/` Tauri v2 shell: sidecar gah + window wired straight to the local service; tray (about / check updates / autostart) / notify / single-instance / system folder picker (state shared with the settings panel: tray actions reach an open panel within 1.5 s); **diagnostics**: `<user data>/gah-shell.log` collects shell events, sidecar stderr (go-plugin `[DEBUG]` filtered), textual sidecar stdout, page-side errors (reported by the frontend via `shell_log`), the **startup channel matrix** (sync / async / picker / autostart probed once each) and **panics from any thread** (`panic @ file:line`); **zero-cost release** (updater with self-held ed25519 signing + CI matrix + unsigned-run first-launch guide) |

## 3. Quick start

### Download and install (recommended for non-programmers)

Grab the right file from [Releases](https://github.com/nekoleamo/go-agent-harness/releases/latest):

| System | File | Notes |
|---|---|---|
| macOS (Apple silicon) | `gah_<version>_aarch64.dmg` | open, then drag gah into /Applications |
| Windows (64-bit) | `gah_<version>_x64-setup.exe` | double-click to install (current user, no admin) |
| CLI (any platform) | `go-agent-harness_<version>_<os>_<arch>.tar.gz` | single binary, unzip and run (also `.zip` + `checksums.txt`) |

> **The first launch is blocked once by the OS** (the app ships without an Apple Developer
> certificate / Microsoft code-signing certificate — the binaries are fine, the system simply has not
> seen them before):
>
> **macOS** has two different messages, and they need different handling:
> - "**cannot verify the developer**" → in /Applications **right-click gah → Open → Open** (once).
> - "**is damaged and can't be opened. You should move it to the Bin**" → **the app is not damaged.**
>   That is macOS's fixed wording for an unsigned/unnotarized app downloaded from the internet (it only
>   carries an ad-hoc local signature). **Right-click → Open does not help here**; run this once in
>   Terminal (after dragging the app into Applications; repeat it after an upgrade if it appears again):
>   ```bash
>   xattr -dr com.apple.quarantine /Applications/gah.app
>   ```
> - **Windows** blue SmartScreen warning → click "**More info** → **Run anyway**" (once)

After installing, just double-click: built-in chat UI plus a settings panel. On first use pick one model
provider (DeepSeek / Kimi / Zhipu / Qwen / Ollama presets; paste an API key).

> **Where the data lives / upgrading and uninstalling**: everything (sessions, memory, schedules, keys) lives in
> `gah-data/`, next to the running binary. On first launch the desktop shell copies the binary into **your user
> data directory** (Windows `%LOCALAPPDATA%\dev.gah.desktop\bin\`, macOS
> `~/Library/Application Support/dev.gah.desktop/bin/`) and runs it from there, so the data sits **outside the
> app directory**: **upgrading (whole-app replacement) keeps it, and uninstalling does not delete it**.
> The desktop shell still auto-backs up to `~/gah-upgrade-backup/` before installing an update and **cancels the
> upgrade** if that backup fails. For manual backups use `/backup <path outside the app dir>` and
> `/backup restore <name>`. The CLI build is unaffected: upgrading replaces the single `gah` file and keeps
> `gah-data/` in place.
> **Two exceptions (the only ways to lose your config)**: ① the Windows uninstaller page has a
> \"Delete app data\" checkbox — ticking it also removes `%LOCALAPPDATA%\dev.gah.desktop` (where the data root
> lives). It is **unchecked by default**; leave it unchecked to keep your data. ② Linux ships as a CLI
> tarball only: the data sits next to `gah`, so **extracting the new version over the same directory** keeps
> it, while replacing the whole directory loses it. Full matrix: `DESIGN.md` R25.

### Build (release form)

```bash
# Direct build (single file, dev version)
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)" -o gah ./cmd/gah
# Or the unified entry (guarantees the embedded frontend; write VERSION=v0.x.y)
./scripts/gen.sh cli
```

### Global install (deploy once, run from any directory, pi-style)

```bash
# macOS / Linux: build → ~/.local/gah + symlink ~/.local/bin/gah (prompts for PATH when missing)
bash scripts/install.sh
# Windows (PowerShell): build → %LOCALAPPDATA%\gah + add to the user PATH (new terminal)
powershell -ExecutionPolicy Bypass -File scripts\install.ps1
```

After install, run `gah` / `gah web` / `gah --profile headless -input "…"` from any directory.
The data root is `gah-data/` next to the real binary (created on first run; a symlinked launch also resolves to the real install dir). Sessions/memory/todo are isolated per project by the current cwd. Upgrade = rerun the script; uninstall = `--uninstall` / `-Uninstall`.

> **Windows prerequisite**: the `shell` tool and background jobs run through a POSIX shell (`sh -c`),
> so Windows needs [Git for Windows](https://git-scm.com/download/win) (it ships `bash.exe`; probed at
> `%ProgramFiles%\Git\bin\bash.exe`, `%ProgramFiles(x86)%\Git\bin\bash.exe`,
> `%LOCALAPPDATA%\Programs\Git\bin\bash.exe`, then `bash.exe` on `PATH`), or set `GAH_SHELL_PATH`
> explicitly. There is **no cmd.exe/PowerShell fallback** — write-target adjudication is POSIX-lexical,
> and swapping the shell would decouple the sandbox decision from what actually runs; when no POSIX
> shell is found the tool fails **explicitly** (never degrades silently).

**Two modes coexist (same binary; the mode is the location)**: ① **Global/shared** — after install, run `gah` from any directory; the data root lives once under the install dir's `gah-data/`, with sessions/memory isolated per project by the cwd. ② **Portable single** — just copy/download `gah` into any directory and run it there; that directory auto-creates its own independent `gah-data/`, fully isolated from the global install. They never interfere; no switching needed.

### Three run forms

```bash
./gah                                        # TUI (default tui profile; auto-falls back to text when non-TTY)
./gah web                                    # Web UI (≡ --profile web), http://127.0.0.1:2233, opens browser
./gah --profile headless --input "run echo hi"        # one headless turn (CI/scripts; mock model needs no key)
./gah --profile mcp-serve                    # MCP-server form (launched over stdio by an external MCP client)
./gah --profile dev --dump-config            # print the merged config tree (any entry is patchable)
./gah --ephemeral --profile headless --input "hi"    # throwaway data root (isolated testing)
./gah --version                              # version
```

> `--ephemeral` puts all runtime data (incl. the external plugin dir) into a temp home that is deleted on exit — good for isolated CI smoke tests.

### Use a real model

```bash
export DEEPSEEK_API_KEY=sk-...          # or OPENAI_API_KEY / ANTHROPIC_API_KEY (for claude-* models)
./gah --profile headless --input "你好"
```

For production, copy `config/profile-headless.yaml` to your own profile; in a patch, disable `llm-mock` and enable `llm-openai-compat` (set `data.base_url/model`). Keys can also be written to `provider.yaml` via `/provider` (below) instead of shell env.

### Add any provider instantly (inside TUI; zero config edits, zero restarts)

Any OpenAI-compatible endpoint (DeepSeek/SiliconFlow/Ollama/vLLM/Kimi…) needs one line:

```
/provider set <baseUrl> <apiKey> [model]    # effective + persisted immediately (provider.yaml, 0600)
/provider show                              # show current endpoint/model/credentials (masked)
/provider unset base_url|api_key|model      # remove one field (falls back to env/sample; keeps the rest)
/provider clear                             # wipe everything + reset at runtime
```

Example (SiliconFlow):

```
/provider set https://api.siliconflow.cn/v1 sk-<your-key> deepseek-ai/DeepSeek-V3
```

Then chat normally; use `/provider clear` to return to env-var config.

### Common CLI flags

| Flag | Effect |
|---|---|
| `-profile <name>` | profile (tui/headless/dev/web/mcp-serve/custom) |
| `-input <text>` | headless single input: run one turn, print the reply, exit |
| `-dump-config` | print the merged config tree and exit |
| `-ephemeral` | throwaway data root |
| `-version` | print version |
| `-install <repo>[@version]` | install online/bridge plugins (`mcp:<id>:<command>` registers MCP plugins) |
| `-uninstall <id>` / `-list-plugins` | uninstall / list external plugins |
| `-install-ui <repo\|local dir>` / `-uninstall-ui <id>` / `-list-ui-plugins` | UI-plugin install / uninstall / list |

Document reading subcommand (same level as `web`/`im`; zero assembly, read-only):

```bash
gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--max-input-bytes B] [--tree] [--depth N]
# exit codes: 0 ok / 2 usage / 3 unsupported format / 4 over budget / 5 parse failure
# --convert (optional): with LibreOffice installed, converts legacy binary Office (.doc/.xls/.ppt)
# to PDF first; artifacts land in $GAH_HOME/cache/doc/ (kept 7 days); failures fall back explicitly
```

## 4. TUI commands

> Commands register into the host `ctx.commands` and work from TUI/Web/headless with a `/` prefix; typing `/` pops the command palette (name+description, filterable). **Every command supports step-by-step confirmation**: arguments cascade as declared (subcommand enums → dynamic candidates such as providers/sessions/plugins/jobs/backups/themes/IM groups; free-form args break into input prompts) — the TUI uses its picker, and Web uses the same registry-declared candidate list (click through the levels). TUI-only commands (search/widgets/theme/help/exit/fork/clone/tree/name) are available in the TUI only.

| Command | Effect |
|---|---|
| `/model <name>` | Switch model (**dynamically enumerates all models of the current endpoint**, source annotated like `(siliconflow)`; auto-switches to that provider; manual input fallback when listing fails or no key) |
| `/thinking off\|low\|medium\|high` | Thinking budget; **Shift+Tab cycles forward**; the status bar shows the thinking label (hidden when off) |
| `/sandbox ro\|ws\|full` | Switch sandbox tier at runtime (read-only / workspace-write / full-access; live status bar shows the *effective* tier and, when the approval tier overrides it, the source; preference persists across restarts) |
| `/approval open\|smart\|strict` | Switch approval tier at runtime (open=allow dangerous cmds / smart=confirm dialog (default) / strict=deny; persists) |
| `/provider show\|add\|use\|set\|unset\|clear` | Configure LLM providers (multi-provider): `show` list (active ★ credentials masked) / `add endpoint key [model]` (first becomes active) / `use <name>` switch / `set …` edit active / `unset field` remove one (env/sample fallback) / `clear` wipe & reset |
| `/plugins list\|on\|off <id>` | Runtime plugin toggles (`on/off` persist; `default` restores the config-tree default) |
| `/settings history N\|off\|unlimited` | Session-history injection count (off=disabled / unlimited=all / N=recent N; global preference) |
| `/compact [hint]` | Manual rolling summary compaction (auto over-budget compaction unchanged; hint is only recorded) |
| `/export [path]` | Export current session events (`.html` suffix = self-contained HTML, otherwise jsonl) |
| `/workspace [dir]` | Switch workspace (project): pick from recent list or type a new dir; opens a new session, really chdirs tool processes, syncs sandbox root |
| `/session list\|switch\|new\|current` | Session management: list (★ = pinned, with summary) / switch (picker with content preview + time) / new (empty history) / current |
| `/session pin\|unpin [id]` | Pin / unpin a session (defaults to the current one; pinned block sorts first, max 8) |
| `/session summary [id]` | Generate/show the session summary (LLM: one-line + topics; **calls the model**) |
| `/reload` | Hot-reload instruction files (AGENTS.md hierarchy/global/extra; external edits apply without restart) |
| `/jobs list\|output <id>\|kill <id>` | Background jobs: list / output / kill (same source as workflow `background` and the Web jobs panel) |
| `/schedule [list]\|add <cron> <description>\|rm\|on\|off\|run <id>` | Scheduled tasks: list / add (5-field cron, e.g. `0 8 * * *` = daily at 08:00) / remove / enable-disable / run once now (same source as the settings panel "Plans" section) |
| `/backup [dest]\|list\|restore <name>` | Full GAH_HOME backup (config incl. keys/plugins/sessions/env.sh/prefs, excludes backups/ itself): no arg = back up now (default `$GAH_HOME/backups/`, external path allowed) / `list` (newest first) / `restore <name>` (**auto-backs-up the current state first**; fully effective after restart) |
| `/preview <path>` | Open the document preview workbench: TUI shows a full-screen pager (`↑↓`/`PgUp`/`PgDn` scroll, `←→` horizontal, `/` search with `n/N`, `q`/`Esc` close); Web opens the document panel at that file (markdown/text/code/CSV/notebook/docx/xlsx/pptx/PDF) |
| `/search <word>` | In-session search (hits highlighted; n/N/F3 cycle; Esc exits) |
| `/theme [name]` | Switch theme (enumerates `$GAH_HOME/config/themes/*.yaml`; `default` resets; no recompile; persists) |
| `/fork [seq]` | Derive a branch session from any point in history (`/tree` shows seqs; default = latest question) |
| `/clone` | Clone the current session (a second path in the same branch) |
| `/tree` | Session branch tree (full picture + forkable question points) |
| `/name <display name>` | Give the current session a display name (`-` clears; takes priority in the status bar / switch list) |
| `/widgets on\|off` | Toggle the widget strip above the input (dynamic host-registered info lines) |
| `/help` `/exit` | Help / quit (**press Ctrl+C twice**, anti-misfire) |

> **Key cheat-sheet**: a single `Ctrl+C` while typing only clears the input (does not quit); with empty input you must press `Ctrl+C` **twice within 2s** to quit — the first press highlights a "press again" hint, and the timer resets on timeout or any other key; `Esc` cancels the running turn; `Shift+Tab` cycles thinking levels; `Ctrl+T` folds/expands thinking blocks; `Ctrl+O` folds the latest tool result; `Ctrl+↑/↓` jumps to the earliest user line / back to bottom; `Ctrl+A` select-all (delete = clear / typing = replace), `Ctrl+B/F` move left/right, `Ctrl+Y` redo, `Alt+P` yank-paste, `Alt+←/→` word-wise move.

## 5. Model-callable tools (invoked by the model, no interaction)

| Tool | Description |
|---|---|
| `shell` | Run shell commands (sandbox/approval policies intercept; `data.pty` drives interactive processes; credential env stripped) ; write targets are path-adjudicated (out-of-workspace writes denied under workspace-write, every write denied under read-only) ; **env jail**: `TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` are always redirected to `$GAH_HOME/jail/**` (`HOME`/`GOPATH` are left alone; `GAH_SHELL_JAIL=0` disables) ; **kernel-level sandbox** (group 3): the host passes the *effective* tier down to the execution entry, and `shell` restricts file **writes at the process-tree level** — macOS `/usr/bin/sandbox-exec` (seatbelt), Linux **Landlock** (kernel >= 5.13, applied by a self re-exec helper since Landlock is irreversible); allow-list = the workspace root the effective tier permits + `$GAH_HOME/jail/**` + required device nodes (paths are symlink-resolved first), and `read-only` keeps the jail writable; platforms without the capability (e.g. Windows) get a one-time stderr warning and no wrapper; `GAH_SHELL_KERNEL_SANDBOX=0` disables; **POSIX shell resolution** (`sdk/shellpath.go`): `GAH_SHELL_PATH` > Git for Windows common install locations / `PATH` on Windows > `sh`, shared with background jobs, explicit error when missing (no silent degradation), and MSYS argument path conversion is disabled on Windows (`MSYS_NO_PATHCONV`) so adjudicated paths match what actually runs) |
| `file_read` / `file_write` / `file_append` / `file_edit` | Read/write/append/precise-edit files (sandbox path validation) |
| `web_fetch` / `web_search` | Fetch URL content / web search (default Exa, `EXA_API_KEY`; `data.provider` swappable; 401/429/5xx structured errors) |
| `workflow` / `workflow_collect` | Restricted starlark composing multi-step tool calls (natively sandboxed); `background` async + collect |
| `job_list` / `job_output` / `job_kill` | Background job query/output/kill (same source as `/jobs`) |
| `memory` | Cross-session memory (remember/list/recall/forget; `$GAH_HOME/memory/<project>.jsonl`, human-editable) |
| `todo` | Task list (create/start/complete/pend/delete/update/list; 4-state machine + blockedBy deps; `$GAH_HOME/todos/`) |
| `auto_plan` | Planning mode (create/get/list/step/confirm/complete; detects planning intent, outputs a structured plan first, zero side-effect tool calls before confirmation; `$GAH_HOME/plans/`) |
| `subagent` | Subagent delegation (delegate/spawn/agents/agent_status/agent_kill/send_message/fork; isolated ReAct contexts, background handles) |
| `list_skills` / `read_skill` | Skill index / load SKILL.md on demand (project `.gah/skills/`, `$GAH_HOME/skills/`) |
| `mcp_<server>_<tool>` | MCP bridge tools (`mode: direct`; see "MCP" below) |
| `mcp_search` / `mcp_call` | MCP search-mode proxy tools (`mode: search`): search the tool list by keyword (empty query = all), then invoke by name |

## 6. Web usage (settings panel / REST)

`gah web` serves http://127.0.0.1:2233 (auto-opens the browser; disable with `GAH_WEB_OPEN=0`). Feature-equivalent to the TUI, plus visual panels:

**Auth (token mode = non-empty `data.auth_token`)**: every surface is gated — API, static assets, `/attachments/` and `/ui-plugins/` all require a credential (previously only `/api/*` was protected). Open the `#token=<token>` URL printed in the startup log: the credential sits in the **URL fragment** (never sent to the server, absent from access logs/Referer), the bootstrap page exchanges it via `POST /api/auth` for an HttpOnly + SameSite=Strict `gah_token` cookie, then redirects into the UI. The desktop shell passes the same token via `GAH_WEB_TOKEN` (`?token=` is deprecated).

- **Settings panel** (⚙ in the status bar): model dropdown (all providers aggregated), thinking/sandbox/**approval** segmented controls, history-injection dropdown + compact button, provider management (enable/delete/add; **first-run onboarding** auto-opens the panel when no provider exists and offers one-click presets for DeepSeek/Kimi/GLM/Qwen/SiliconFlow/OpenRouter/OpenAI/Ollama that need only an api_key, followed by a **connectivity self-check** that renders 401/404/DNS endpoint errors as plain language next to the raw response), **scheduled plans** ("Plans" section: 5-field cron + Chinese "next run time" read-back, run/enable/delete; the unattended-run semantics are stated in that section), **MCP servers** ("MCP server" section: per-entry name/command/enable/mode (full registration or on-demand search), read-only rows for env-provided servers, per-row "loaded, N tools" state, and **Save and reload** for immediate effect), plugin toggles, instruction reload, **data backup** (back up now / restore — **double-confirmed**). Every change persists on exit (gah-state.json, shared with the TUI).
- **Sidebar**: pinned workspaces (switching = real chdir) + session history (name/content preview/time; ✎ rename, × delete — double-confirmed), attachment upload (button/drag-drop/paste; image thumbnails + the model sees images), session export (⤓ jsonl/HTML).
- **Status bar**: connection state (green/orange), model/thinking/sandbox/session, context·cache usage, background-jobs button (running badge + list/output/kill).
- **Document preview panel** (sidebar "Document preview"): workspace file tree on the left (filter / lazy expand / whole tree resets on workspace switch) and preview on the right (markdown block rendering, code/tables, docx/xlsx/pptx block model, native in-browser PDF viewer, images, HTML **source view by default with a click-to-load sandboxed iframe**; truncation and warning strips); tool result rows with a previewable path get a "Preview" button; assistant text in the session stream is markdown-rendered server-side (zero `v-html` in the frontend).
- **WebSocket channel**: `/api/events/ws` (same payload as SSE; the frontend degrades automatically).
- **General REST surface** (callable by the frontend or scripts; missing services return explicit 503/501):

| Method / path | Description |
|---|---|
| `POST /api/auth` | Bootstrap channel: `{"token":"…"}` (or `Authorization: Bearer`) → `gah_token` cookie (POST-only, 401 on a wrong token); the only path exempt from the auth gate, still under the Host allow-list + same-origin guard |
| `GET /api/state` | State snapshot (model/thinking/sandbox/approval/stats/session/running/version) |
| `POST /api/input` | Submit a turn; `/` prefix routes to commands; 409 while running |
| `POST /api/confirm` | Approval reply `{id, ok}` |
| `GET /api/events` + `GET /api/events/ws` | Event stream (SSE replay on reconnect / WS) |
| `GET /api/sessions`, `POST /api/sessions` | Session list / `{action: switch\|new\|fork\|clone\|delete}` |
| `POST /api/sessions/rename`, `GET /api/sessions/{id}/export` | Rename / export jsonl |
| `GET /api/workspaces`, `DELETE /api/workspaces/{key}` | Workspace history / delete record (keeps the folder) |
| `GET /api/commands`, `POST /api/commands/{name}` | Command registry / direct execution `{args}` |
| `GET /api/tools`, `POST /api/tools/{name}` | Tool listing / invocation (JSON args pass-through) |
| `GET /api/jobs`, `GET/POST /api/jobs/{id}[/kill]` | Background jobs |
| `GET/POST /api/schedules`, `PATCH/DELETE /api/schedules/{id}`, `POST /api/schedules/{id}/run` | Scheduled plans: list / add / update (only the fields sent) / delete / run now |
| `GET/POST /api/mcp` | MCP server config: GET returns the config plus per-entry runtime state (loaded / tool count); POST submits the full list = write `config/mcp.yaml` and restart the plugin (`reload:false` writes only; a save that succeeds while the reload fails returns 200 + `reload_err`) |
| `GET /api/plugins`, `POST /api/plugins/{id}/load\|unload` | Plugin toggles |
| `GET /api/models?all=1`, `GET/POST /api/providers...` | Model aggregation / provider CRUD |
| `POST /api/control` | Status-bar-level control `{model?\|thinking?\|sandbox?\|approval?\|workspace?}` (persists) |
| `POST /api/compact`, `POST /api/settings/history` | Manual compaction / history-injection count |
| `GET /api/todo` | Todo panel data (pass-through of the todo tool list) |
| `GET /api/backup`, `POST /api/backup` | Backup list / `{action: backup\|restore, dest?, name?}` |
| `POST /api/attachments` + `GET /attachments/...` | Attachment upload (20MB / type allowlist) / static preview |
| `POST /api/reload` | Instruction-file hot reload |
| `POST /api/shutdown` | Graceful shutdown (→ system/shutdown → DisposeAll; reused by desktop/ops) |
| `GET /api/ui-plugins` + `/ui-plugins/` | UI-plugin aggregate view / static hosting; aggregate items carry `trusted`/`trust_note` (UI plugins share the app's origin and therefore its privileges: they can call any API, including `/api/input` → tool execution; only install plugins you trust; the SPA ships a strict CSP that closes pure exfiltration) |
| `GET /api/doc/preview` `raw` `asset` `tree` `html`, `POST /api/doc/render` | Document preview (block-model JSON) / raw bytes (Range, `dl=1` download) / embedded assets (MIME allow-list) / file tree / sandboxed HTML (CSP) / markdown text → block model |

## 7. Configuration & runtime directories

### Config layer (profile→bundle→patch)

```
profile-<name>.yaml     # bundles + patches, declared in order
bundle-base.yaml        # plugin entries (id + enabled + data params)
patch-*.yaml            # replace/insert/toggle entries by id (hot pluggable)
```

Samples live in `config/` (two copies kept in sync with `internal/embed/seed`; `# seed-version: N` header; bump on new base entries). Any entry printed by `--dump-config` can be replaced by your own patch.

### Runtime data root (GAH_HOME, fully portable)

The data root is **the `gah-data/` sibling of the gah binary (the only one; auto-created on first run, releasing config samples + bundled plugins — the deployment directory is self-contained)**. If creation fails (read-only/unwritable dir) startup **fails with an explicit error**. Neither `GAH_HOME env` nor `~/.gah` acts as a data-root source anymore (tightened 2026-09-16; after boot GAH_HOME is set internally for plumbing, see cmd/gah homeDir).

```bash
# 1) Drop gah into any directory; the first run auto-creates gah-data/ and releases config samples + plugins:
./gah --profile web
#    Migrating existing data (e.g. from the old ~/.gah): mkdir gah-data && cp -a ~/.gah/. gah-data/
# 2) Upgrades replace the single gah file only; data stays put; if the dir is read-only, set GAH_HOME or move to a writable dir
```

- Multiple gah copies can share one gah-data (single root; never system root / scattered cwd); explicit override: `GAH_HOME=/path ./gah ...`.
- Keys (provider.yaml / search.yaml) live in `gah-data/config/`, backed up and moved with the directory.
- **Full backup**: `/backup` packs the whole gah-data (excluding backups/ itself) → default `gah-data/backups/` (moves with the dir) or an external path; `/backup restore <name>` restores (auto-backup of the current state first).

### Environment variables

| Variable | Effect |
|---|---|
| `GAH_HOME` | Internal plumbing variable (set by boot to the portable `gah-data/`; plugins/external processes derive subdirs from it); **user-set values are ignored** (the only data root is the sibling `gah-data/`, since 2026-09-16; a different user-set value logs a warning) |
| `GAH_PROFILE` / `GAH_NO_TUI` | Default profile / force TUI off (headless/CI) |
| `GAH_WEB_ADDR` / `GAH_WEB_OPEN` / `GAH_WEB_STATIC` | Web listen address (default 127.0.0.1:2233) / auto-open browser / static dir override (dev HMR); the desktop shell additionally uses `GAH_WEB_TOKEN` to pass the token and navigate to the `#token=` URL (its readiness probe treats 401 as ready) and **picks a free port** to override the listen address via `GAH_WEB_ADDR` (a fixed port can land on someone else's instance: an orphan sidecar or the user's own `gah web`), setting `GAH_WEB_PARENT_WATCH=1`: once the parent dies the sidecar exits gracefully (no orphan holding the data root) |
| `GAH_MCP_COMMAND` / `GAH_MCP_COMMANDS` | MCP bridge (single server / multi server one per line `name=command args`); **`$GAH_HOME/config/mcp.yaml` wins** — same-name entries taken from the file (env-only entries show as read-only "env" rows in the settings panel) |
| `GAH_CB_ADDR` / `GAH_CB_TOKEN` | host-bridge callback channel (external plugins request tools/jobs/fanout from the host; auth token; **never leaked downstream** — `SanitizedEnv` strips it) |
| `GAH_SHELL_KERNEL_SANDBOX` | `0` disables the shell **kernel-level sandbox** (enabled by default: macOS `sandbox-exec` seatbelt / Linux Landlock restrict file writes at the process-tree level; writes only — reads and network are untouched; platforms without the capability warn once and degrade to the cooperative layer) |
| `GAH_SHELL_JAIL` | `0` disables the shell **env jail** (enabled by default: redirects `TMPDIR`/`XDG_CACHE_HOME`/`GOCACHE`/`GOMODCACHE`/`npm_config_cache`/`PIP_CACHE_DIR` into `$GAH_HOME/jail/**` so build caches and temp files stop littering the user home directory; `HOME`/`GOPATH`/`CARGO_HOME`/`XDG_CONFIG_HOME` are deliberately preserved so git/ssh keep working) |
| `GAH_EXT_ENV_PASS` | Explicit allow-list of env vars passed to external plugin processes (comma-separated): external plugins do **not** inherit host credentials by default (`*_API_KEY`/`*_TOKEN`/`AWS_*`/`GAH_CB_*` are stripped); name the ones a plugin genuinely needs (e.g. `EXA_API_KEY`), or use a config file (recommended: `$GAH_HOME/config/search.yaml`) |
| `GAH_MCP_SERVE` / `GAH_PLUGIN` / `GAH_VERSION` | External tool-process entry params (serve/load plugin/version announcement; injected when host-bridge launches them) |
| `DEEPSEEK_API_KEY` / `OPENAI_API_KEY` / `ANTHROPIC_API_KEY` | LLM keys (per-provider prefix routing; or write to provider.yaml via `/provider`) |
| `EXA_API_KEY` | web_search key (default provider; switch via `data.provider`) |

### MCP (bridge client / serve form)

Two ways to configure (precedence: config file > env vars; the GUI writes the file):

```yaml
# $GAH_HOME/config/mcp.yaml (same source the settings panel writes)
servers:
  - name: deja
    command: /opt/homebrew/bin/deja
  - name: codegraph
    command: codegraph serve --mcp
    mode: search      # many tools? use search: the model only sees mcp_search/mcp_call
```
```bash
# Env vars (startup surface, read-only; tools auto-register as mcp_<server>_<name>)
export GAH_MCP_COMMANDS="deja=/opt/homebrew/bin/deja\ncodegraph=codegraph serve --mcp"
./gah --profile web

# As an MCP server exposing all repository tools (launchable by Claude Desktop etc. over stdio)
./gah --profile mcp-serve
```

### Plugin install (capability expansion beyond TUI/Web)

```bash
./gah -install <repo>[@version]     # external plugin (go-plugin bridge): git clone → build → $GAH_HOME/plugins/<id>/ → idempotent register; enabled right away
./gah -install mcp:<id>:<command>   # MCP plugin registered through the same entry
./gah -install-ui <repo|local dir>  # UI plugin (manifest.json slot overrides; v-html directive scan rejects)
./gah -list-plugins / -uninstall <id>
./gah -list-ui-plugins / -uninstall-ui <id>
```

### Instruction files & skills

- **Instructions**: global `$GAH_HOME/AGENTS.md` + project `AGENTS.md` (closer overrides farther; `AGENTS.override.md` replaces at the same level) — auto-injected into the system prompt; `/reload` hot-reloads.
- **Skills**: project `.gah/skills/` + global `$GAH_HOME/skills/`, SKILL.md scanning; the model loads them on demand via `list_skills`/`read_skill`.
- **Themes**: `$GAH_HOME/config/themes/*.yaml` (`/theme` to switch, no recompile; the repo ships a gruvbox-dark sample).

## 8. Plugin development

Follow [docs/PLUGIN_DEV.md](./docs/PLUGIN_DEV.md) (registered in-repo as the `gah-plugin-dev` skill, so a running gah can read it on demand):

1. `plugins/<type>-<name>/`, implement `sdk.Plugin` (Start registers side effects, returns a Disposer)
2. Register in `plugins/catalogue` (provides/requires/bundle)
3. Add an entry to `config/bundle-base.yaml` (enable/data params) + sync `internal/embed/seed` and bump seed-version
4. Unit tests + full `-race` green before committing

Red lines: plugins import **only `sdk/`** — never core/tui/other plugins; registration is a side effect, unloading revokes it. Tool plugin bodies are externalized to `extplugins/` by default (crash isolation, independent upgrades); built-in implementations are disabled.

## 9. Project layout

```
├── cmd/gah/          # boot: profile assembly entry + CLI flags (profile/input/install/ephemeral…)
├── core/             # microkernel: ctx container / event (5 dispatch) / plugin (topology+hot reload) / config (profile→bundle→patch)
├── sdk/              # the only dependency of plugins: interfaces + domain models (standalone module)
├── bundles/          # bundle assembly (base/tui/web/register)
├── plugins/          # all plugins: host/adapter/policy/tool/mcp/ui (catalogue/ is the single source of truth)
├── extplugins/       # out-of-process plugin entrypoints (tool-basic/tool-workflow/tool-mcp/tool-subagent/tool-echo)
├── tui/              # bubbletea v2 UI (testable state machine; ui-tui-app thin shell)
├── web/              # Web server Go runtime (SSE/WS + REST + static embed web/dist)
├── web-src/          # frontend project (Vue3+Vite+TS; build embedded; UI-plugin samples in web-src/examples/)
├── tests/            # e2e + mini MCP server (unload matrix per AGENTS.md)
├── internal/         # embed (seed samples/external plugins) / install / prefs / providerfile
├── config/           # profile/bundle/patch samples (seed-version kept in sync with internal/embed/seed)
├── scripts/          # gen.sh (unified build)/ gen-web.sh / gen-extplugins.sh / gen-desktop.sh / ws-smoke.go / publish-desktop.sh
├── desktop/          # desktop shell P1 (Tauri v2 + sidecar gah; zero-cost release: updater+CI)
├── .gah/skills/      # self-registered skills (gah-plugin-dev)
└── docs/             # local design docs (only PLUGIN_DEV.md ships with the repo; the rest are local materials)
```

## 10. Build & release

```bash
./scripts/gen.sh cli             # single binary for this platform (TUI+Web+headless; frontend embedded; skips rebuild when unchanged)
./scripts/gen.sh release         # goreleaser six-target release (tar.gz/zip + checksums)
./scripts/gen.sh desktop         # desktop shell: sidecar (= cli artifact) → tauri build (.app/.dmg, unsigned)
./scripts/gen.sh web-dev         # frontend hot dev (vite watch + GAH_WEB_STATIC, no restarts)
```

One gah binary, three surfaces: the Web frontend is embedded; TUI/Web/headless are three profiles of one process; the desktop shell reuses the same sidecar artifact. Release matrix:

- `scripts/gen-extplugins.sh` builds external plugin artifacts for the matrix (darwin/linux × amd64/arm64 + windows/amd64), `gzip -9 -n` deterministic compression, embed split per platform (each target embeds only its own).
- `goreleaser release --snapshot` produces all six target bundles directly; `.goreleaser.yaml` configures before hooks.

Measured acceptance (DESIGN §7.6 / latest baseline): `CGO_ENABLED=0` static single file **41–46 MiB** across five targets (gate = `scripts/size-check.sh`, currently ≤48 MiB / gz ≤32 MiB), six-target cross-compilation green, bare `env -i` boot OK, sha256 attached.

## 11. License

**MIT License** (see [LICENSE](./LICENSE), © 2026 nekoleamo): permissive — use, modify, and distribute commercially under closed source.
