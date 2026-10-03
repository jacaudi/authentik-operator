# Security Policy

## Reporting a vulnerability

Please report vulnerabilities privately using GitHub's
**"Report a vulnerability"** button on the repository's Security tab
(private vulnerability reporting). Do not open a public issue.

Include what you found, how to reproduce it, and the impact you see. You
should get an acknowledgement within a few days.

## Supported versions

The project is pre-1.0 (API `v1alpha1`); only the latest release receives
security fixes.

## Scope notes

The operator holds Authentik API tokens and reads Kubernetes Secrets. The
trust boundary and RBAC model are documented in `docs/design/scope.md`
("Decided: connection model"). Reports about privilege escalation through
custom resources (for example making the operator read or send a Secret the
requester cannot access) are in scope.
