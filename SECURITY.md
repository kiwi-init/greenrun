# Security

Report vulnerabilities privately through GitHub Security Advisories for
`kiwi-init/greenrun`.

Greenrun executes repository workflows with access to Docker. A workflow must
be treated as executable code with the same trust level as the repository.

Greenrun:

- never reads dotenv files unless `--secret-file` names one explicitly;
- never forwards the user's GitHub CLI token into workflow jobs;
- masks explicitly supplied values and runtime `add-mask` commands;
- stores runs with user-only filesystem permissions;
- verifies release checksums in the installer;
- publishes runner images with SBOM, provenance, vulnerability scanning, and
  keyless signatures.
