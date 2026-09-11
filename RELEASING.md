# Releasing

A release is cut by pushing a tag. Everything else is automated by
[`.woodpecker/release.yml`](.woodpecker/release.yml).

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

Woodpecker then builds six targets, signs every artefact with cosign, publishes a GitHub release,
and pushes the Homebrew **cask** to [`ciphera-net/homebrew-tap`](https://github.com/ciphera-net/homebrew-tap).

> 🔴 **The first release after the cask migration (v1.1.1) needs one manual step in the tap.**
> Read [Migrating the tap](#migrating-the-tap-one-time-at-v111) before tagging. Skipping it publishes a
> cask that nobody can install, with no error to say so.

## Required secrets

Configured on the repository in Woodpecker, scoped to the `tag` event only — a release credential has
no business being available to a pull request from a fork.

| Secret | Source | Status |
|---|---|---|
| `cosign_private_key` | Vault `kv/pulse-cli/cosign` → `private_key` | ✅ configured |
| `cosign_password` | Vault `kv/pulse-cli/cosign` → `password` | ✅ configured |
| `release_github_token` | A GitHub token, Contents:write on `pulse-cli` + `homebrew-tap` | ✅ configured 07-08-2026 (fine-grained; browser-only to create, see below) |

### Why this one cannot be automated

Recorded for whoever has to **rotate** it. `release_github_token` has to be created **in a browser**.
There is no way around it:

- **GitHub removed programmatic PAT creation.** The old `POST /authorizations` endpoint returns
  `404`, and there is no REST endpoint for fine-grained tokens.
- **The deploy-key fallback is blocked.** goreleaser can push the Homebrew formula over SSH instead
  of a token, and a deploy key *can* be created via the API — but this org returns
  `422 "Deploy keys are disabled for this repository"`, and changing that needs `admin:org`.
- **Reusing an operator's `gh` OAuth token is worse, not easier.** That token carries `delete_repo`,
  `gist` and account-wide `repo`; a scoped release token carries Contents:write on two repositories.
  Putting the broader credential in CI to save 30 seconds trades a large blast radius for a small
  convenience.

### Creating it

**Fine-grained token** (preferred — smallest scope):
<https://github.com/settings/personal-access-tokens/new>

- Resource owner: **`ciphera-net`**
- Repository access: **only** `ciphera-net/pulse-cli` and `ciphera-net/homebrew-tap`
- Permissions: **Contents: Read and write** on both
- Expiry: whatever your rotation cadence is — the pipeline fails loudly when it lapses

A classic PAT with `repo` also works, but `repo` grants access to **every** repository the account can
reach, which is exactly what the fine-grained form exists to avoid.

Then install it:

```bash
WT=$(grep '^WOODPECKER_TOKEN=' .env | cut -d= -f2- | tr -d '"')
curl -X POST -H "Authorization: Bearer $WT" -H "Content-Type: application/json" \
  -d '{"name":"release_github_token","value":"<token>","events":["tag"]}' \
  https://ci.ciphera.net/api/repos/51/secrets
```

`events: ["tag"]` matters: a release credential has no business being readable by a pull request.

**Until it exists, a tag push will fail at the preflight check rather than half-publishing** — the
pipeline verifies every secret is present before it builds anything, so the failure costs a pipeline
run and nothing else.

## Tool pinning — one pin retired, one kept

The release step runs in `ghcr.io/goreleaser/goreleaser:v2.17.1`, which already carries goreleaser,
Go 1.26.5, git and cosign. It replaced `golang:1.25` + `go install goreleaser`, which cannot work:
goreleaser 2.17.1 needs Go >= 1.26.5 and that image sets `GOTOOLCHAIN=local`.

Two deliberate pins used to sit on top of that image. Both were re-tested on **07-08-2026**. One is
now gone; the other stays, and the tests that keep it are written out below so nobody has to
re-litigate it from scratch.

### RETIRED: `brews` → `homebrew_casks`

The reason `brews` was kept was that **casks were macOS-only and a formula covered Linux too**. That
was true when it was written. It is no longer true: Homebrew 6.0.0 (11-06-2026) added Linux support
for portable cask artifacts, and the cask goreleaser generates uses exactly one artifact — `binary`,
which the Cask Cookbook lists as usable on either operating system.

Measured on the generated cask, on **Homebrew 6.0.15**, loaded from a local tap — not argued from
release notes:

| Check | Result |
|---|---|
| `on_linux` block present with both Linux arches | ✅ `linux_amd64` + `linux_arm64` URLs and sha256s |
| `Cask#supports_linux?` | ✅ `true` (so `search` / `info` / `bundle` advertise it on Linux) |
| `SimulateSystem.with(os: :linux, arch: :intel)` → resolved URL | ✅ `…_linux_amd64.tar.gz` |
| `SimulateSystem.with(os: :linux, arch: :arm)` → resolved URL | ✅ `…_linux_arm64.tar.gz` |
| Only artifact type | ✅ `Cask::Artifact::Binary`; `depends_on.macos` is `nil` |

Two things fell out of that work and are recorded where they matter:

- **`goreleaser check` now exits 0**, so both pipelines gate on the **exit code** again instead of
  grepping for `configuration is valid`. The grep was a workaround for the deliberate deprecation; it
  would also have swallowed the *next* deprecation silently.
- **`pulse upgrade` had to learn the cask layout.** A formula stages into
  `…/Cellar/pulse/<v>/bin/pulse`; a cask stages into `…/Caskroom/pulse/<v>/pulse`. The Homebrew guard
  matched `/Cellar/` only, so the first cask release would have made it stop firing — silently, with
  no error and no failing build, leaving `pulse upgrade` free to overwrite a brew-managed binary.
  `internal/upgrade.Detect` now matches both, and both arms are covered by tests that were confirmed
  to fail when their arm is removed.

### KEPT: cosign v2.4.1, ahead of the v3 in the image

The image now ships **cosign v3.1.2**. It still cannot produce the signature this project publishes.
Re-tested 07-08-2026 inside `ghcr.io/goreleaser/goreleaser:v2.17.1` itself:

| Attempt (v3.1.2) | Result |
|---|---|
| The exact `.goreleaser.yaml` args (`--output-signature` + `--tlog-upload=false`) | ❌ `Error: must specify --bundle with --new-bundle-format` — nothing written |
| `--bundle --new-bundle-format --tlog-upload=false` | ❌ `Error: --tlog-upload=false is not supported with --signing-config or --use-signing-config` |
| `--new-bundle-format=false` | ❌ same `--tlog-upload` error |
| `signing-config create --no-default-rekor --no-default-fulcio --no-default-oidc --no-default-tsa` then `--signing-config … --bundle --new-bundle-format` | ✅ **signs, with no transparency log** — but emits a Sigstore *bundle* |
| That bundle, fed to the README's `verify-blob --key … --signature …` | ❌ `Error: invalid signature when validating IEEE_P1363 encoded signature` |
| `--signing-config … --output-signature=…` (to get a detached sig back) | ❌ `Error: must specify --bundle with --new-bundle-format` |

So: v3 **can** sign without Rekor — that part of the old note is now out of date, and a signing-config
file is the mechanism — but there is **no combination that emits the detached `.sig` the documented
`verify-blob --signature` command reads.** Migrating would mean publishing bundles and rewriting the
README's verification command, which is a user-visible break in the one instruction this project asks
people to trust. The answer is still: keep the pin.

**The pin costs users nothing.** Also measured in that image, signing with v2.4.1 and verifying with
each version, using the README command verbatim:

| | good archive | tampered archive |
|---|---|---|
| cosign v2.4.1 verifies a v2.4.1 signature | ✅ `Verified OK` | ✅ fails: `invalid signature when validating ASN.1 encoded signature` |
| cosign **v3.1.2** verifies a v2.4.1 signature | ✅ `Verified OK` (with a `--signature has been deprecated` warning) | ✅ fails: same error |

A user on cosign v3 can still verify our releases with the command in the README. Only the *signer*
has to be v2.

> A trap worth remembering, and it fired again during this re-test: a tamper-detection check
> *passes* when signing failed entirely, because there is no valid signature for anything. Under v3
> both the good and the tampered artifact "failed" — with an identical parse error. Any test that a
> bad input is rejected is meaningless unless you also confirm the good input was accepted.

**When to revisit.** `--signature` is deprecated on the *verify* side too, so a future cosign
(v4-shaped) may drop it and break verification for anyone on a current release. The escape hatch is
known: sign into a bundle via a no-tlog `--signing-config`, publish `.bundle` alongside `.sig`, and
change the README to `verify-blob --bundle`. `pulse upgrade`'s embedded verification would move with
it. Do that when v2 stops being installable, not before — every published release's `.sig` has to
stay verifiable.

## The signing key

Generated 2026-08-07, stored in Vault at `kv/pulse-cli/cosign`. The public half is committed as
[`cosign.pub`](cosign.pub) and is what users verify against.

The release pipeline **derives the public key from the private one and compares it to `cosign.pub`
before building**. A mismatch stops the release. Without that check, a rotated key would publish
signatures that fail verification against the key the README points at — and the first person to find
out would be a user, not us.

Verified end to end at generation time: sign → verify with the public key alone → **and a modified
archive fails**. A signature that verifies proves nothing if it also verifies something else.

### Rotating it

1. `cosign generate-key-pair` (a fresh random password, not a reused one).
2. `vault kv put kv/pulse-cli/cosign private_key=@cosign.key password=… public_key=@cosign.pub`.
3. Update the two Woodpecker secrets.
4. **Commit the new `cosign.pub` in the same change.** The preflight check will otherwise stop the
   next release, which is the intended behaviour but a confusing way to be reminded.
5. **Copy it to `internal/upgrade/cosign.pub` too.** `pulse upgrade` verifies against a key compiled
   into the binary — fetching the key at upgrade time would mean whoever can serve a modified archive
   can serve the key that signs it. `TestEmbeddedKeyMatchesRepoRoot` fails if the two copies drift,
   so this is a failing test rather than a silent divergence, but it is one more file to update.

Old signatures do not re-verify under a new key. Note the rotation in the release notes so anyone
verifying an older archive knows which key to use.

A rotation also breaks `pulse upgrade` for everyone still running a binary built with the old key:
their embedded copy will refuse the newly signed archive, correctly and unhelpfully. They are not
stranded — `brew upgrade`, `go install …@latest` and a manual download all still work — but the
release notes for a rotation should say so, because "signature does not match the Pulse release key"
reads like a compromise rather than a key change.

## No transparency log

`--tlog-upload=false`. cosign publishes to the Sigstore transparency log by default, and Rekor is
operated in the US — the standing rule is that no new US service enters the critical path without
explicit sign-off, and release signing is squarely in that path.

The cost is real: without a log there is no public append-only record of everything this key has
signed, so verification rests on trusting `cosign.pub` from this repository. Users need
`--insecure-ignore-tlog=true` when verifying, which is documented in the README along with what it
does and does not skip.

Reversing this is a one-line change to `.goreleaser.yaml` plus a README edit.

**Re-reviewed and reaffirmed 07-08-2026.** The decision stands: sovereignty wins, the log stays off.
Recording it here so the flag reads as a choice rather than an oversight and is not "fixed" later by
someone who assumes it was forgotten.

One thing changed that raises the stakes. `pulse upgrade` (v1.1.0) verifies release signatures
against a copy of `cosign.pub` **embedded in the binary**, so with no transparency log that embedded
key is the *entire* trust root for self-update. Two consequences:

- **Rotating the signing key breaks `pulse upgrade` for every already-installed binary**, because
  they carry the old key and will correctly reject artifacts signed with the new one. Those users
  must reinstall through their package manager. See "Rotating it" above — the embedded copy at
  `internal/upgrade/cosign.pub` must be updated in the same change, and a test fails if the two
  copies drift.
- A compromise of the signing key would be undetectable after the fact, since there is no append-only
  record to audit against. That is the price of the sovereignty call, stated plainly.

## Version stamping

`main.version` is set by ldflags from the tag. A binary built any other way reports `dev`, which is
the intended answer — a locally built binary is not a release, and it should not claim to be one.

## Validate before tagging

`goreleaser check` runs on every pull request (`.woodpecker/build.yml`) and again in the release
pipeline. To run the whole thing locally without publishing anything:

```bash
go install github.com/goreleaser/goreleaser/v2@v2.17.1
goreleaser check
goreleaser release --snapshot --clean --skip=publish,sign
```

The snapshot builds all six targets, writes the archives and checksums, and generates the Homebrew
cask into `dist/homebrew/Casks/pulse.rb` — everything the real release does except publishing and
signing. **v1.0.0's first attempt failed on a config schema error that this would have caught in two
seconds.**

Read the generated cask, do not just note that it was written. It must still carry an `on_linux`
block with `linux_amd64` **and** `linux_arm64` URLs:

```bash
grep -A6 on_linux dist/homebrew/Casks/pulse.rb
```

`goreleaser check` exits non-zero on deprecation warnings as well as errors. The config carries no
deprecated properties any more, so both pipelines gate on the **exit code**. Do not reintroduce a
grep-for-`configuration is valid` gate: it passes on any future deprecation, which is exactly how a
removed-next-version setting reaches a tag unnoticed.

## Migrating the tap (one time, at v1.1.1)

Releases up to v1.1.0 published a **Formula**; v1.1.1 on publishes a **Cask**. goreleaser writes the
new file but cannot delete the old one, and both cannot coexist.

Measured on Homebrew 6.0.15 with `Formula/pulse.rb` and `Casks/pulse.rb` both present in a tap:
`brew install ciphera-net/tap/pulse` resolves to the **formula** and only prints
`Warning: Treating … as a formula. For the cask, use … or specify the --cask flag.` The cask is
published, never installed, and never updated — silently. With the formula removed, the same command
resolves straight to the cask with no flag and no warning.

So, immediately after the v1.1.1 release pipeline goes green:

```bash
# in ciphera-net/homebrew-tap, on a branch
git rm Formula/pulse.rb
```

`Formula/` is otherwise generated by goreleaser and must never be hand-edited — this deletion is the
exception, and it is a deletion, not an edit.

**Existing `brew install`ed users do not migrate themselves, and Homebrew has no mechanism that makes
them.** `tap_migrations.json` only redirects to a *different* tap: `Formulary` guards the migration
branch on `tapped_name != new_tapped_name`, so a same-tap `{"pulse": "ciphera-net/tap"}` entry is a
no-op. Verified — with that entry in place and only a cask in the tap, the formula lookup still raised
`TapFormulaUnavailableError: No available formula with the name "…/pulse"`.

The v1.1.1 release notes must therefore say, in the installation section:

```bash
brew uninstall pulse && brew install ciphera-net/tap/pulse
```

`pulse upgrade` already prints `brew upgrade pulse` for both layouts, so nobody's binary gets
overwritten in the meantime — but `brew upgrade` alone will not move them across.

## 🔴 After the release: install it the way a user does

**Not optional, and it is the step that would have caught the worst bug this project has
shipped.** v1.1.1, v1.1.2 and v1.2.0 each published a macOS cask whose binary **could not run at
all** — `pulse --version` printed nothing and exited **137**. A cask stages a downloaded file, so
macOS writes `com.apple.quarantine` on it; the binary is ad-hoc signed only
(`codesign -dv` → `Identifier=a.out`), so Gatekeeper rejects it and the kernel kills the process
before `main()`. Brew reports success. The shell prints nothing. There is no error anywhere.

Every artifact check passed the whole time, and each one was real: the cosign signature verified
with its negative, the SHA256 matched the cask and `checksums.txt` three ways, `brew info` showed
the new version, and the binary extracted from the tarball by hand ran perfectly — **because
extracting a tarball by hand does not set quarantine.** Checking the artifact and installing the
artifact are different code paths, and only one of them is the one users take.

It also hid behind a blind spot worth remembering: the only machine testing carried a **formula**
install from before the tap migration, and formulae do not carry quarantine.

```bash
# From a shell that has never seen the new version:
brew uninstall --cask pulse 2>/dev/null
git -C "$(brew --repository ciphera-net/tap)" fetch origin && \
  git -C "$(brew --repository ciphera-net/tap)" reset --hard origin/main
brew install --cask ciphera-net/tap/pulse

pulse --version          # MUST print the new version and exit 0
pulse mcp --help         # MUST succeed — proves the subcommand shipped
xattr -l "$(readlink "$(which pulse)")" | grep -c quarantine   # MUST be 0
```

⚠️ `spctl -a -t exec -vv <binary>` still reports **rejected** after a successful install. That is
expected, not a regression: the binary is genuinely unsigned, and Gatekeeper only *enforces* on
quarantined files. The postflight removes the trigger, not the unsignedness. Do not "fix" the
postflight because spctl still complains.

⚠️ `brew uninstall pulse` can fail with *"Refusing to load cask from untrusted tap"* while a
formula and a cask share the name, leaving BOTH installed with the old binary still on `PATH`.
Use `brew uninstall --formula pulse` explicitly, then `brew reinstall --cask`.

The real fix is Apple codesigning plus notarisation, which needs a paid Developer ID. Until that
is an owner decision made and paid for, the cask `postflight` in `.goreleaser.yaml` is the only
thing standing between a release and a binary nobody can run — treat deleting it as a breaking
change.

## Pre-release checklist

- [ ] `go test ./...` green, and the mutation battery still red-on-mutation if guards were touched
- [ ] The cross-compile and smoke pipelines are green on `main`
- [ ] `goreleaser check` exits **0** (not "printed something reassuring")
- [ ] `goreleaser release --snapshot --clean --skip=publish,sign` succeeded, and the cask in
      `dist/homebrew/Casks/pulse.rb` still has `linux_amd64` and `linux_arm64` URLs
- [ ] `README.md` matches the actual command surface, including any new flags
- [ ] The version in any documentation example is not a stale one
- [ ] For a **first** release: `release_github_token` exists
- [ ] **For v1.1.1 only:** the tap migration above is scheduled — `Formula/pulse.rb` deleted right
      after the release, and the reinstall line in the release notes
- [ ] **After tagging:** the post-release install check above was run, `pulse --version` printed the
      new version from a `brew install --cask`, and the quarantine count was 0
