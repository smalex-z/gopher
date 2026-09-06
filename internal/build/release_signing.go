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
// signed or updaters will refuse it. The repo has immutable releases enabled
// (a published release's assets freeze instantly), so CI creates stable
// releases as DRAFTS and scripts/sign-release.sh signs the draft and
// publishes it as its final step — the signature is always inside before the
// freeze, and updaters never see a stable release unsigned. Prerelease
// channels (alpha/beta) publish directly from CI, unsigned; the updater
// verifies a signature when present but tolerates its absence there, so dev
// velocity is unaffected.
//
// Losing the secret key means edges running signature-checking builds cannot
// auto-update to anything you can no longer sign — back the key file up (it
// is itself password-encrypted) before setting this.
//
// A var, not a const: a const makes every enforcement branch in
// verifyReleaseSignature dead code that no test can reach (Go tests can't
// override a const), so the policy's first real execution would be on a
// production edge. Tests inject a throwaway keypair here; production code
// never assigns it. scripts/sign-release.sh greps this assignment for its
// pre-upload sanity verify — keep the `ReleaseSigningPubKey = "..."` shape.
var ReleaseSigningPubKey = "RWSWPp2QiyIyyl9oVckRlaysVm0aQGZpZrPd5cqEFjQwx6TX6FhDE50l"
