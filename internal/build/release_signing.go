package build

// ReleaseSigningPubKey is the minisign public key gopher release checksums are
// signed with — the base64 line from minisign.pub (e.g. "RWT..."), generated
// once with `minisign -G` on the maintainer's machine and NEVER stored on
// GitHub, in CI secrets, or on any edge. It is the trust root of the update
// pipeline: with it set, Apply() refuses to install a stable release whose
// SHA256SUMS.txt is not accompanied by a SHA256SUMS.txt.minisig that verifies
// against this key. That moves the authority to publish installable releases
// from "any credential with write access to the GitHub repo" (which can
// create releases and replace assets on existing ones, silently) to
// "possession of the offline secret key".
//
// Empty = verification disabled (pre-signing builds keep today's behavior).
// Once a release ships with this set, every later stable release MUST be
// signed (scripts/sign-release.sh — one command after the CI publish) or
// updaters will refuse it. Prerelease channels (alpha/beta) verify the
// signature when present but tolerate its absence, so dev velocity is
// unaffected.
//
// Losing the secret key means edges running signature-checking builds cannot
// auto-update to anything you can no longer sign — back the key file up (it
// is itself password-encrypted) before setting this.
const ReleaseSigningPubKey = ""
