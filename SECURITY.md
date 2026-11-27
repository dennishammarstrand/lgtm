# Security

Please report vulnerabilities privately through [GitHub's vulnerability reporting](https://github.com/dennishammarstrand/lgtm/security/advisories/new), not in a public issue.

## Verifying a download

Release archives are built by GitHub Actions from a tagged commit, with signed build provenance. To check that a file came from this repository:

```sh
gh attestation verify lgtm_0.2.1_darwin_arm64.tar.gz -R dennishammarstrand/lgtm
```

`checksums.txt` in each release lists the SHA-256 of every file.
