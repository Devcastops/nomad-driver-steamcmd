# nomad-driver-steamcmd

A Nomad task driver that wraps `steamcmd` for installing/updating
Steam-distributed dedicated servers, then optionally launching and
supervising the resulting binary as the task's tracked process.

## Design decisions (and why)

- **No filesystem/network isolation (`FSIsolationNone`, host networking
  only).** This is deliberately `raw_exec`-style rather than
  container-style. It means CSI volumes staged by Nomad are visible to the
  task with zero extra plumbing — no bind-mounting `TaskConfig.Mounts`
  into a chroot — and it sidesteps container-network DNS/IPv6 flakiness
  entirely, since the task talks to Steam's CDN over the host's network
  stack directly.
- **Every task gets its own `install_dir`** (defaults to `local/steamapp`,
  inside Nomad's per-allocation task dir). This avoids steamcmd's known
  flakiness with concurrent installs sharing one appcache. It costs
  duplicate downloads per server; if that becomes a real problem, put a
  transparent caching proxy (e.g. lancache) in front of the Steam CDN
  rather than adding shared-install locking to the driver.
- **`StartTask` is synchronous through the install phase, matching how the
  built-in `docker`/`qemu` drivers handle slow starts (image pulls, VM
  boot).** Nomad only persists a task's recoverable `DriverState` once
  `StartTask` returns, and there's no mechanism for a driver to push
  updated state into that record afterwards -- so the install can't happen
  in a detached goroutine if `RecoverTask` is going to reattach correctly
  later. `StartTask` now blocks until steamcmd finishes and (if a `launch`
  block is configured) the server process has actually started, and only
  then persists state -- so a recovered handle always reflects a real PID,
  never a stale "was installing" snapshot. Progress is visible in the
  meantime the same way Docker surfaces image-pull progress: raw steamcmd
  output streams to the task's log files immediately (`nomad alloc logs`),
  and a throttled (every 5s) human-readable summary is emitted as
  `TaskEvent`s (`nomad alloc status`), parsed from steamcmd's own
  `Update state (0x..) downloading, progress: NN.NN` lines.
- **`RecoverTask` only has one real case to handle: reattaching to an
  already-launched process by PID.** Because a handle is only ever
  persisted post-install, a driver/client crash *during* an install means
  Nomad never received a handle for that task in the first place --
  Nomad's own client just calls `StartTask` again from scratch, same as it
  would for any driver interrupted before `StartTask` returns. There's no
  "resume a partial install" case to reconcile.
- **steamcmd's exit code is not trusted.** It's well known to return 0 on
  partial/failed installs and nonzero on some successful ones. `StartTask`
  parses stdout for explicit `Success! App '<id>' ...` / `ERROR!` /
  auth-failure markers instead. See `driver/steamcmd.go`.
- **Auth is sourced generically, not Vault-specifically.** `login.password`
  and `login.password_file` are plain fields; how the value gets there
  (Vault via `template`, Nomad variables, or a literal) is entirely up to
  the job author. The driver has no secrets-backend dependency.
- **Client-level default login.** A `plugin "steamcmd" { config { ... } }`
  stanza in the Nomad agent config can set a fleet-wide `default_login`
  (typically `anonymous = true`). A task's own `login` block, if present,
  is a full override — no field-level merging with the default in v1.
- **Cron-based restarts are intentionally NOT a driver feature.** Use
  Nomad's own `periodic` batch-job stanza to call
  `POST /v1/allocation/:id/restart` on a schedule instead. Keeps the
  driver's job restricted to "run steamcmd + supervise a process."
- **Disk-space preflight.** `StartTask` checks free space on `install_dir`
  before invoking steamcmd and fails fast with a clear task event rather
  than letting steamcmd hang/fail confusingly mid-download.

## Task config reference

```hcl
config {
  app_id          = "2394010"        # required, Steam App ID
  install_dir     = "local/steamapp" # default shown
  beta            = ""               # optional beta branch
  beta_password   = ""               # optional, only if beta requires one
  validate        = false            # steamcmd `validate` flag
  update_on_start = true             # re-run steamcmd before every launch
  install_timeout = "20m"            # optional, Go duration string

  login_anonymous     = true   # or:
  login_username      = ""
  login_password      = ""     # literal, or:
  login_password_file = ""     # path to a file (e.g. rendered by `template`)

  launch {                 # omit for an install-only task
    command = "local/steamapp/PalServer.sh"
    args    = ["-port=8211"]
    env     = { KEY = "value" }
  }
}
```

Login fields are flat top-level attributes, not a nested `login {}` block.
That's not a style choice -- a nested block here was found to decode
incorrectly specifically in the Nomad client agent's plugin-config parsing
path (flat attributes decoded fine; the nested block's contents silently
came back as zero values even when set in HCL). See `driver/config.go` for
the full note.

## Plugin (client agent) config reference

```hcl
plugin "steamcmd" {
  config {
    steamcmd_path                = "steamcmd" # must be on the node's PATH
    install_root                 = ""         # reserved, unused in v1
    max_concurrent_installs      = 0          # 0 = unbounded
    default_login_anonymous      = "true"     # STRING "true"/"false", not a bare bool -- see note below
    default_login_username       = ""
    default_login_password       = ""
    default_login_password_file  = ""
  }
}
```

`default_login_anonymous` takes the *string* `"true"`/`"false"`, not a bare
HCL boolean -- but that's not actually the important caveat here.

**⚠️ Confirmed limitation: explicit values in this agent-config block may
not reach the plugin at all.** Debugging traced a persistent CI failure
down to `default_login_anonymous` decoding as `"false"` -- a genuine
5-character string, matching this schema's own default literal exactly --
even though the agent's `nomad.hcl` clearly set it to `"true"`. The field
isn't failing to decode; it's silently falling back to whatever default
this driver declares, regardless of what's actually written in the config
file. `steamcmd_path` appeared to work throughout earlier debugging only
because its explicit value happens to equal its own default, making a
silent fallback and a genuine explicit value indistinguishable.

This looks like a limitation in Nomad v1.9.3's agent-config loading for
plugin config blocks generally, not something specific to this driver's
schema -- but that's inferred from observed behavior, not confirmed
against Nomad's source or issue tracker. **If you need the plugin-level
default to be anything other than anonymous login, don't rely on this
block** -- set `login_username`/`login_password` explicitly on every task
instead (`TaskConfig`'s fields go through job-spec parsing, a different
Nomad code path that has not shown this problem). `default_login_anonymous`
now defaults to `"true"` in the schema itself specifically so the common
case works even if your agent config's explicit setting is silently
ignored. See the `PluginConfig` doc comment in `driver/config.go` for the
full investigation.

## Building

```sh
make build      # -> dist/plugins/nomad-driver-steamcmd
make test       # unit tests (no Nomad/steamcmd required)
make dev-agent  # single-node `nomad agent -dev` with the plugin loaded
```

> **Note on this repo's origin:** this codebase was scaffolded in a
> sandboxed environment without access to `proxy.golang.org` or the
> various `golang.org/x/...` vanity-import domains Nomad's dependency
> tree pulls in, so `go mod tidy`/`go build` could not be fully verified
> there. Every file is confirmed `gofmt`-clean (syntax-valid Go). The CI
> workflow (`.github/workflows/ci.yml`) runs on a normal GitHub-hosted
> runner with unrestricted network access and does the real
> `go mod tidy && go build && go test` — treat a green CI run as the
> actual first compilation, and expect to fix any real API-surface
> mismatches against `github.com/hashicorp/nomad@v1.9.3`'s current
> `plugins/drivers` interface that a from-memory scaffold can't
   guarantee against (see `driver/lifecycle.go`, `driver/driver.go`).

## CI

- **`ci.yml`** — `go vet`, `gofmt -l`, unit tests with race detector and
  coverage, build the plugin binary.
- **`e2e.yml`** — the real thing:
  1. Installs a real Nomad binary and a real `steamcmd` (via apt,
     `i386` arch + EULA auto-accepted via `debconf-set-selections`).
  2. Starts a single-node `nomad agent -dev` with this plugin loaded and
     `default_login_anonymous = "true"`.
  3. Confirms the driver **fingerprints healthy**.
  4. Runs a real install-only job against a small, anonymous-downloadable
     app (Half-Life Dedicated Server, App ID `90`) and asserts the task
     exits cleanly and files actually land on disk.
  5. Runs a **deliberate bad-credentials job** and asserts it fails
     cleanly rather than hanging (exercises the auth-failure parsing
     path).
  6. Runs a long-running "launch" job, then kills and restarts the Nomad
     agent, and confirms the task is still reported `running` afterward
     (exercises `RecoverTask`'s PID-reattach path).
  7. Uploads all Nomad/task logs as artifacts regardless of outcome.

  CSI is deliberately **not** exercised end-to-end in CI (it would need an
  external NFS server or an in-CI CSI plugin, which is disproportionate
  infra weight for what the driver itself needs to guarantee — see
  `driverCapabilities` in `driver/driver.go`, `MountConfigSupportAll` +
  `FSIsolationNone` is the whole contract, and that's exercised by the
  install-job's `local/` task-dir mount already going through the same
  no-isolation code path a CSI mount would).

## Known gaps / things to harden before production use

- `StartTask` blocks for the full install duration -- for a large game
  server on a slow link this could be many minutes. This is intentional
  (see design notes above) but means `nomad job run` itself will appear to
  hang for that long; progress is visible via `nomad alloc status`/`nomad
  alloc logs` in another terminal in the meantime.
- No shared-install dedup across tasks on the same node (by design, see
  above) — revisit only if disk usage becomes a real problem.
- `password`/`password_file` are read once at `StartTask` time; rotating a
  credential requires a task restart, same as any other Nomad `template`
  driven secret.
