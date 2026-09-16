# Disabled workflows

Distribution workflows parked here so GitHub Actions ignores them. This fork
publishes GitHub Releases (via `.github/workflows/release.yml`) but does not
publish to Homebrew, PyPI, or npm.

To re-enable one, move it back to `.github/workflows/`.

- `update-homebrew.yml` — updates the Homebrew tap when a release is published
- `test-pypi.yml` — manual test publish of the MCP package to PyPI
