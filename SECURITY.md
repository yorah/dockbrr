# Security Policy

## Supported versions

Only the latest release receives security fixes. dockbrr versions below 1.0
have no LTS branches.

## Reporting a vulnerability

Please report vulnerabilities privately via GitHub's private vulnerability
reporting: go to the [Security tab](https://github.com/yorah/dockbrr/security)
and click "Report a vulnerability". Do not open a public issue for anything
you believe is exploitable.

You can expect an acknowledgement within a week. Please include reproduction
steps and the dockbrr version.

## Scope notes

- dockbrr holds the Docker socket, which is root-equivalent on the host.
  Anything that lets an unauthenticated or lower-privileged party reach
  dockbrr's mutating API is in scope and treated as critical.
- dockbrr is designed to run on a trusted network or behind an
  authenticating reverse proxy. Reports assuming the attacker is already an
  authenticated dockbrr admin are generally out of scope (single-user model).
- The registry credentials and GitHub token stored by dockbrr are encrypted
  at rest (AES-256-GCM, key in `secret.key`). Reports involving an attacker
  with full read access to the data directory are out of scope: protecting
  the data directory is the deployment's job.
