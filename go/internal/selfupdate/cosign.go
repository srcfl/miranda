package selfupdate

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// cosignOIDCIssuer is the OIDC issuer that mints the token Fulcio signs against
// for GitHub Actions jobs. Pinning it stops a cert minted via some OTHER issuer
// (a different CI, a forged token endpoint) from satisfying verification.
const cosignOIDCIssuer = "https://token.actions.githubusercontent.com"

// cosignIdentityRegexp builds the Subject Alternative Name we require the
// checksums.txt signing cert to carry: this repo's release workflow, on a
// version tag. GoReleaser's `signs:` block runs in exactly that context, so the
// short-lived Fulcio cert it obtains has this SAN. Anyone else (a fork, a
// different workflow, a push that is not a tag) gets a different SAN and fails.
func cosignIdentityRegexp(repo string) string {
	return fmt.Sprintf("https://github.com/%s/.github/workflows/release.yml@refs/tags/v.*", repo)
}

// verifyChecksumsSignature establishes the PROVENANCE of checksums.txt before
// any digest inside it is trusted.
//
// Trust model: the per-archive SHA256 in checksums.txt only protects against a
// corrupted download. It says nothing about WHO produced the file — an attacker
// who can publish a release could swap an archive and rewrite its checksum in
// lockstep. checksums.txt.sig + checksums.txt.pem are a cosign keyless
// signature; `cosign verify-blob` confirms, against Sigstore's trust roots and
// the Rekor transparency log, that checksums.txt was signed by a Fulcio cert
// whose identity is this repo's release workflow on a tag (sigIdentity), issued
// from a GitHub Actions OIDC token (cosignOIDCIssuer). A pass means checksums.txt
// genuinely came from THIS repo's release pipeline. It does NOT attest to the
// source that was built — only to the checksum file's origin.
//
// Degradation policy:
//   - cosign not on PATH       -> stay SILENT and return nil (checksum-only
//     fallback). Most users don't have cosign installed, and the per-file SHA256
//     already guards against a corrupted download, so a successful update must not
//     look like a failure. Set MIR_REQUIRE_COSIGN to turn this into a hard error.
//   - signing assets absent    -> if the release predates signing (no .sig/.pem
//     URLs at all), fall back silently (or hard-fail under MIR_REQUIRE_COSIGN). If
//     cosign is present AND the assets are expected but unfetchable, hard failure.
//   - verification fails        -> return error; the caller MUST abort the update.
//   - verification passes       -> emit a positive one-line confirmation via note.
//
// note receives a single human-readable line (no trailing newline) for the success
// confirmation; pass a stderr writer in production, nil to silence.
func (c *Client) verifyChecksumsSignature(rel *Release, sums []byte, note func(string)) error {
	emit := func(msg string) {
		if note != nil {
			note(msg)
		}
	}
	// soft degrades a missing-provenance case: silent by default (don't nag the
	// majority without cosign), a hard error when the operator demands verification.
	soft := func(reason string) error {
		if os.Getenv("MIR_REQUIRE_COSIGN") != "" {
			return fmt.Errorf("%s, and MIR_REQUIRE_COSIGN is set", reason)
		}
		return nil
	}

	if _, err := exec.LookPath("cosign"); err != nil {
		return soft("cosign is not installed, so the release signature was not verified")
	}

	// A release cut before signing was introduced carries no .sig/.pem. cosign
	// being installed cannot conjure them — fall back rather than hard-fail so
	// upgrading FROM an old release still works. (The next signed tag is the
	// first one that will actually exercise verification.)
	if rel.ChecksumsSigURL == "" || rel.ChecksumsCertURL == "" {
		return soft("this release has no cosign signature")
	}

	sig, err := c.fetch(rel.ChecksumsSigURL)
	if err != nil {
		return fmt.Errorf("download checksums signature: %w", err)
	}
	cert, err := c.fetch(rel.ChecksumsCertURL)
	if err != nil {
		return fmt.Errorf("download checksums certificate: %w", err)
	}

	dir, err := os.MkdirTemp("", "mir-cosign-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	sumsPath := filepath.Join(dir, "checksums.txt")
	sigPath := filepath.Join(dir, "checksums.txt.sig")
	certPath := filepath.Join(dir, "checksums.txt.pem")
	for _, f := range []struct {
		path string
		data []byte
	}{{sumsPath, sums}, {sigPath, sig}, {certPath, cert}} {
		if err := os.WriteFile(f.path, f.data, 0o600); err != nil {
			return err
		}
	}

	// #nosec G204 -- args are constants / our own temp paths, not user input.
	cmd := exec.Command("cosign", "verify-blob",
		"--certificate", certPath,
		"--signature", sigPath,
		"--certificate-identity-regexp", cosignIdentityRegexp(c.Repo),
		"--certificate-oidc-issuer", cosignOIDCIssuer,
		sumsPath,
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cosign verify-blob failed for checksums.txt (possible tampering): %w", err)
	}
	emit("✓ verified the release signature (cosign keyless)")
	return nil
}
