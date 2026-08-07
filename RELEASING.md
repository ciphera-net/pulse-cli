# Releasing

A release is cut by pushing a tag. Everything else is automated by
[`.woodpecker/release.yml`](.woodpecker/release.yml).

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

Woodpecker then builds six targets, signs every artefact with cosign, publishes a GitHub release,
and pushes the Homebrew formula to [`ciphera-net/homebrew-tap`](https://github.com/ciphera-net/homebrew-tap).

## Required secrets

Configured on the repository in Woodpecker, scoped to the `tag` event only — a release credential has
no business being available to a pull request from a fork.

| Secret | Source | Status |
|---|---|---|
| `cosign_private_key` | Vault `kv/pulse-cli/cosign` → `private_key` | ✅ configured |
| `cosign_password` | Vault `kv/pulse-cli/cosign` → `password` | ✅ configured |
| `release_github_token` | A GitHub token, Contents:write on `pulse-cli` + `homebrew-tap` | ⚠️ **not yet configured — browser only, see below** |

### Why this one cannot be automated

`release_github_token` has to be created **in a browser**. There is no way around it:

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

## Tool pinning — both pins are load-bearing

The release step runs in `ghcr.io/goreleaser/goreleaser:v2.17.1`, which already carries goreleaser,
Go 1.26.5, git and cosign. It replaced `golang:1.25` + `go install goreleaser`, which cannot work:
goreleaser 2.17.1 needs Go >= 1.26.5 and that image sets `GOTOOLCHAIN=local`.

**cosign is then pinned BACK to v2.4.1, ahead of the v3 the image ships.** This is not tidiness.
**cosign v3 changed the signing contract**: with `--tlog-upload=false` it refuses to run without
`--bundle --new-bundle-format`, and what it produces is not readable by the
`verify-blob --signature` command this project's README documents.

Verified inside that exact image before pinning: with v3 the sign step errors and writes nothing;
with v2.4.1 it signs, verifies against the committed public key, and rejects a modified file. The
pipeline asserts the version rather than printing it — if v3 were still first on `PATH`, every
signature published would be unverifiable by the documented command.

> A trap worth remembering: a tamper-detection check *passes* when signing failed entirely, because
> there is no valid signature for anything. Any test that a bad input is rejected is meaningless
> unless you also confirm the good input was accepted.

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

Old signatures do not re-verify under a new key. Note the rotation in the release notes so anyone
verifying an older archive knows which key to use.

## No transparency log

`--tlog-upload=false`. cosign publishes to the Sigstore transparency log by default, and Rekor is
operated in the US — the standing rule is that no new US service enters the critical path without
explicit sign-off, and release signing is squarely in that path.

The cost is real: without a log there is no public append-only record of everything this key has
signed, so verification rests on trusting `cosign.pub` from this repository. Users need
`--insecure-ignore-tlog=true` when verifying, which is documented in the README along with what it
does and does not skip.

Reversing this is a one-line change to `.goreleaser.yaml` plus a README edit.

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
formula into `dist/` — everything the real release does except publishing and signing. **v1.0.0's
first attempt failed on a config schema error that this would have caught in two seconds.**

`goreleaser check` exits non-zero on deprecation warnings as well as errors, and `brews` is
deliberately deprecated-but-kept — so both pipelines test for the `configuration is valid` line
rather than the exit code.

## Pre-release checklist

- [ ] `go test ./...` green, and the mutation battery still red-on-mutation if guards were touched
- [ ] The cross-compile and smoke pipelines are green on `main`
- [ ] `README.md` matches the actual command surface, including any new flags
- [ ] The version in any documentation example is not a stale one
- [ ] For a **first** release: `release_github_token` exists
