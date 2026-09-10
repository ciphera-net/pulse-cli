# pulse

Read your [Pulse](https://ciphera.net/products/pulse) analytics from the terminal.

A single static binary with no runtime to install. Read-only, aggregates-only, and it stores your API
key in the operating system keychain rather than in a file.

```console
$ pulse stats --last 7d
  ciphera.net · 1 Aug – 7 Aug 2026 (UTC)

  Visitors          1,284
  Pageviews         3,401
  Bounce rate       62.4%
  Avg duration      1m 47s
  Avg scroll depth  59.7%
  Avg visible time  24s
```

## Install

```bash
brew install ciphera-net/tap/pulse          # macOS and Linux
go install github.com/ciphera-net/pulse-cli/cmd/pulse@latest
```

Or download a signed archive from [releases](https://github.com/ciphera-net/pulse-cli/releases) —
macOS, Linux and Windows, amd64 and arm64. See [Verifying a release](#verifying-a-release).

> **Upgrading from v1.1.0 or earlier?** The tap moved from a Homebrew formula to a cask in v1.1.1.
> Homebrew does not switch you across on its own, so `brew upgrade` will stop finding new versions.
> Run this once:
>
> ```bash
> brew uninstall pulse && brew install ciphera-net/tap/pulse
> ```
>
> Everything else is unchanged, Linux included — the cask carries `linux_amd64` and `linux_arm64`
> builds and installs on Homebrew for Linux the same way the formula did.

## Getting started

```console
$ pulse auth login
Paste your API key (create one at Settings → Organization → API Keys):
› ••••••••••••••••••••••••••••••••••••

✓ Stored key "production" (…chju) in the macOS Keychain.
  Organization 2c1f74ec-… · all sites · expires 2026-11-05 (89 days)

$ pulse sites ls
  SLUG               DOMAIN             TIMEZONE         LAST EVENT
  ciphera-net        ciphera.net        UTC              2 min ago
  id-ciphera-net     id.ciphera.net     Europe/Brussels  1 hr ago
  pulse-ciphera-net  pulse.ciphera.net  UTC              4 min ago

$ pulse sites use ciphera.net
✓ Default site set to ciphera.net.
```

`sites use` accepts a slug, a domain, or an id — whichever you remember.

## Commands

| | |
|---|---|
| `pulse auth login · logout · status` | Manage the stored key |
| `pulse sites ls · use <site>` | List sites, set the default |
| `pulse stats` | Aggregate metrics over a range |
| `pulse realtime` | Visitors active right now |
| `pulse export daily · pages` | Bulk CSV or JSON |
| `pulse mcp` | Serve the read tools to an AI assistant (stdio) |
| `pulse upgrade [--check]` | Install the newest release |

Every command takes `--site` to override the default and `--profile` to switch between stored keys.

## Using Pulse from an AI assistant

`pulse mcp` serves the read tools over the Model Context Protocol, so an assistant can answer
questions about your analytics directly. Add it to your host's configuration:

```json
{
  "mcpServers": {
    "pulse": { "command": "pulse", "args": ["mcp"] }
  }
}
```

**No API key belongs in that file.** The server reads the credential `pulse auth login` already
stored, so your key stays in the system keychain rather than in a config file that tends to end
up committed to a repository.

Six tools are exposed, one per endpoint: `pulse_whoami`, `pulse_list_sites`, `pulse_get_stats`,
`pulse_get_realtime`, `pulse_export_daily` and `pulse_export_pages`. All of them are read-only and
annotated as such, and there is nothing else — see *What this deliberately does not do*.

Two properties are worth knowing, because both exist to stop an assistant from telling you
something untrue about your own data:

- **A withheld figure is not sent as `null`.** When the privacy floor applies, the metric fields
  are absent from the result entirely, replaced by a `suppression` object that says in plain words
  that the value is not zero and may be none. A language model shown `"visitors": null` will
  reliably write "0 visitors"; a model shown no field at all cannot.
- **Date ranges are quoted, never computed.** The server resolves a period in the *site's*
  timezone and echoes the resolved dates, which the tools instruct the assistant to repeat rather
  than work out for itself.

## Reading a withheld result

Pulse enforces a minimum cell size. Any filter engages it, and a slice covering fewer than five
visitors is withheld — **including a genuine zero**.

```console
$ pulse stats --last 7d --filter country==BE
  ciphera.net · 1 Aug – 7 Aug 2026 (UTC)

  Visitors          —
  Pageviews         —
  ...

Withheld: this slice covers fewer than 5 visitors, so every metric is reported as —.
That means "fewer than 5, possibly none" — it does not mean zero.
```

`—` is not zero and not an error. A one-visitor slice and an empty slice return **byte-identical**
responses, on purpose: if "withheld" meant "between 1 and 4" while a real zero came back as `0`,
walking a dimension's values would reveal exactly which ones have a live cohort.

In `--json` the metrics are `null` and `meta.suppressed` is `true`. Never render that as `0`.

## Filtering

Repeatable, one flag per constraint:

```bash
pulse stats --last 30d --filter country==BE
pulse stats --last 30d --filter country==BE --filter country==NL   # either country
pulse stats --last 30d --filter country==BE --filter browser!=Firefox
```

Repeating one dimension **widens** the query (OR); different dimensions **narrow** it (AND). A key
may combine at most **two dimensions** — a third is refused, because narrowing that far describes
individuals rather than populations. The CLI catches it before spending a request.

Available dimensions: `page`, `referrer`, `channel`, `country`, `region`, `city`, `browser`, `os`,
`device`, `screen_resolution`, `language`, `timezone`, `utm_source`, `utm_medium`, `utm_campaign`,
`utm_term`, `utm_content`.

`realtime` accepts no filters, permanently: a filtered five-minute window describes one person's
current session.

## Ranges

```bash
pulse stats --last 7d          # 7d · 30d · month · year
pulse stats --from 2026-08-01 --to 2026-08-07
```

Relative periods are resolved **by the server, in the site's timezone**, and the CLI prints the range
the server actually queried. That is why `--last today` is refused rather than guessed: the dashboard
knows more periods than the API publishes, and every published one is a 24-month commitment.

`export` needs explicit dates, so `pulse export daily --last 7d` asks the server to resolve the
period first (one extra quota unit) rather than computing it against your own clock.

## Output

| | |
|---|---|
| *(default)* | Aligned table for reading |
| `--json` | **Exactly** the API response, unmodified — pipe it to `jq` |
| `--csv` | Spreadsheet-friendly |

Colour, progress and warnings go to **stderr**, and colour turns itself off when stdout is not a
terminal. So this is always clean:

```bash
pulse export daily --last 30d > month.csv
pulse stats --last 7d --json | jq '.data.visitors'
```

`--json` returns the API's own bytes rather than a re-encoding. That keeps the CLI usable as a
debugging tool for the API, and stops it from becoming a second, subtly different contract — v1 is
additive-only, so an older CLI meeting a newer field is expected, and passthrough means the field
still reaches you.

## Exit codes

| | | |
|---|---|---|
| `0` | Success | |
| `1` | Unexpected or server error | `server_error` |
| `2` | Bad usage | `invalid_request` |
| `3` | Not authenticated | `unauthorized` |
| `4` | Not found or out of scope | `not_found` |
| `5` | Rate limited | `rate_limited` |

Locally-detected failures use the same code the API would have: a missing credential is `3` whether
the CLI noticed or the server did.

```bash
pulse auth status >/dev/null 2>&1 || { [ $? -eq 3 ] && pulse auth login; }
```

On a `429` the CLI honours `Retry-After` and retries **once**, noting it on stderr. Only a second
failure exits `5`.

Output that could not be written is a failure, not a success. A full disk, a quota, or a `ulimit -f`
cap exits `1` and names the stream and the reason on stderr, so
`pulse export daily > week.csv` can never leave you a truncated file and a `0`. A **closed pipe is
not** a failure — `pulse sites ls | head -3` is the pipeline working, and it stays quiet.

## Credentials

`pulse auth login` writes to the macOS Keychain, libsecret (Linux), or the Windows Credential
Manager. **This tool never writes a key to a file** — not as a fallback, not on a keychain error.

For CI, where no keychain exists, export `PULSE_API_KEY`. It takes precedence over the keychain, so
an explicit export always wins, and `pulse auth status` tells you which one is in use.

`~/.config/pulse/config.toml` holds preferences only — default site, profiles. Never a credential.

## Upgrading

```console
$ pulse upgrade --check
Update available: v1.0.0 → v1.1.0
https://github.com/ciphera-net/pulse-cli/releases/tag/v1.1.0
Run `pulse upgrade` to install it.

$ pulse upgrade
Downloading pulse_1.1.0_darwin_arm64.tar.gz (2.8 MB)…
Verifying the release signature…
✓ pulse v1.1.0 installed at /usr/local/bin/pulse.
```

The archive comes from GitHub's **public** release feed: no API key is sent, and no Pulse quota is
spent. Its cosign signature is checked against the key compiled into the binary you are already
running — not one fetched at the same time as the archive — and a signature that does not verify
means nothing is written at all.

**`--check` exits `0` whether or not an update exists.** It is meant for a cron job, a Makefile or an
agent loop, and all of those treat a non-zero exit as something to escalate; an available upgrade is
news, not a failure. Only a check that could not be *performed* exits non-zero. `--json` gives it to
you as an object:

```console
$ pulse upgrade --check --json
{"current":"v1.0.0","latest":"v1.1.0","update_available":true,"install_method":"binary",
 "path":"/usr/local/bin/pulse","release_url":"…","action":"checked"}
```

**A pulse installed by a package manager is left alone.** Homebrew and `go install` each keep their
own record of what version they put there, and the Cellar (or Caskroom) is writable — so replacing the
file in place *works*, and then `brew list --versions pulse` describes a binary that no longer exists
and the next `brew upgrade` quietly reverts you. `pulse upgrade` detects both Homebrew layouts, cask
and formula, and prints what to run instead:

```console
$ pulse upgrade
pulse v1.0.0 → v1.1.0 is available; installed via Homebrew, so run: brew upgrade pulse
```

If the install directory is not writable, the command says so, names the path, and stops. **It never
uses sudo** — and never suggests you do.

## Verifying a release

Archives are signed with [cosign](https://github.com/sigstore/cosign) against
[`cosign.pub`](cosign.pub) in this repository.

```bash
cosign verify-blob \
  --key https://raw.githubusercontent.com/ciphera-net/pulse-cli/main/cosign.pub \
  --signature pulse_1.0.0_darwin_arm64.tar.gz.sig \
  --insecure-ignore-tlog=true \
  pulse_1.0.0_darwin_arm64.tar.gz
```

`checksums.txt` is published alongside and is itself signed.

Works on both current cosign majors. Releases are signed with cosign v2.4.1, and the command above was
checked against **v2.4.1 and v3.1.2**: both print `Verified OK` for a good archive and both fail on a
modified one. cosign v3 adds a `--signature has been deprecated` warning — the verification it
performs is the same.

**About that flag.** We sign with our own key and do **not** publish to the Sigstore transparency log,
which is operated in the US — Ciphera's stack is deliberately EU/CH-based and a release pipeline is
part of the critical path. `--insecure-ignore-tlog=true` tells cosign not to look for a log entry that
was never created; the signature is still checked against our published key, and a modified archive
still fails. The name of the flag is cosign's, not a description of the check.

The honest trade-off: without a transparency log there is no public append-only record of everything
this key has signed, so verification rests on trusting `cosign.pub` from this repository.

## What this deliberately does not do

- **No write operations.** The API is read-only and the CLI must not imply otherwise.
- **No live tail.** `pulse realtime` returns a count, once. Streaming reads as per-visitor tracking,
  which is the line Pulse does not cross.
- **No configurable API host.** A support answer beginning "just point it at…" is one somebody else
  can also give.
- **No plugin system.** `--json` plus a shell is the extension mechanism.
- **No write tools over MCP.** `pulse mcp` exposes the same read-only surface as the rest of the
  CLI. An assistant driving it can be steered by text it reads elsewhere, and a read-only surface
  bounds the worst outcome of that at a wrong answer rather than a changed account.

## Licence

Apache-2.0. © Ciphera BV.

Wire types live in [`ciphera-net/pulse-api-go`](https://github.com/ciphera-net/pulse-api-go), shared
with the server so a contract mismatch is a compile error.
