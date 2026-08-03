# Disabled workflows

Distribution workflows parked here so GitHub Actions ignores them. This fork
is built by hand and does not publish releases, Homebrew formulas, or PyPI
packages.

To re-enable one, move it back to `.github/workflows/`.

- `release.yml` — tag-triggered goreleaser release (GitHub Releases + Homebrew formula)
- `update-homebrew.yml` — updates the Homebrew tap when a release is published
- `test-pypi.yml` — manual test publish of the MCP package to PyPI
