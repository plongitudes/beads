# Windows Package Manager (winget) Manifest

This directory contains the winget manifest files for publishing beads to the Windows Package Manager.

## Installation (once published)

```powershell
winget install Plongitudes.beads
```

## Manifest Files

- `Plongitudes.beads.yaml` - Version manifest (main file)
- `Plongitudes.beads.installer.yaml` - Installer configuration
- `Plongitudes.beads.locale.en-US.yaml` - Package description and metadata

## Submitting to winget-pkgs

1. Fork https://github.com/microsoft/winget-pkgs
2. Create directory: `manifests/s/Plongitudes/beads/<version>/`
3. Copy the three manifest files to that directory
4. Submit a PR to microsoft/winget-pkgs

Or use the wingetcreate tool:
```powershell
wingetcreate update Plongitudes.beads --version <new-version> --urls <new-url> --submit
```

## Updating for New Releases

When releasing a new version:

1. Update the version in all three manifest files
2. Update the InstallerUrl in the installer manifest
3. Update the InstallerSha256 (get from checksums.txt in the release)
4. Update the ReleaseNotesUrl
5. Submit PR to microsoft/winget-pkgs

### Getting the SHA256

```bash
curl -sL https://github.com/plongitudes/beads/releases/download/v<VERSION>/checksums.txt | grep windows
```
