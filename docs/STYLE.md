# Engineering style

WPX code should be understandable by an operator reading it during an incident.
Documentation is part of correctness, not a cleanup task.

## Comments

Comments explain invariants, failure modes, ownership, and reasons. They should
make the code read like a careful system narrative in the tradition of Redis:
plain language, attention to edge cases, and no assumption that the reader has
the original author available.

Do not paraphrase obvious syntax. Explain why a path is safe, why an operation
order prevents data loss, and which state may exist after interruption.

## Functions and packages

- Prefer small packages with one operational responsibility.
- Keep policy separate from mechanism.
- Accept typed values rather than loosely related strings.
- Avoid shell interpretation. Use fixed executable paths and argument arrays.
- Make destructive operations idempotent and test interruption boundaries.
- Return errors with operation context while preserving the underlying cause.
- Keep interfaces at the consumer boundary and only when multiple implementations
  or focused tests justify them.

## Tests

Tests should prove invariants, not mirror implementation lines. Privilege and
path-validation tests are mandatory for every broker operation. System behavior
uses containers where possible and disposable Ubuntu 24.04 VMs where kernel,
systemd, firewall, permissions, or package-manager behavior matters.

## Compatibility and recovery

The broker protocol has an explicit version. The browser routes are internal
application routes, not a promised stable public API. State migrations run in
transactions; a release that changes persisted state must explain compatibility
with its predecessor and verify the upgrade recovery path.

Generated configuration carries a WPX ownership marker. Refuse unmanaged files
instead of guessing that their contents are safe to replace. When an operation
spans SQLite, files, services, or a remote provider, document the commit boundary
and what can remain after interruption. Do not call it atomic or idempotent
without explaining the actual mechanism and limits.

Useful comments answer concrete recovery questions. For example: “Keep the old
tree until the imported database passes checks; if the second rename fails, the
old tree is still available at this path.” Avoid comments such as “rename the
directory” that repeat the code without explaining why the order matters.

## Interface styling

Use Tailwind as a utility-first system. Templates carry the utilities that
describe their layout and appearance; reading the markup should reveal how the
interface is rendered.

- Do not use BEM class names.
- Do not recreate components with custom semantic aliases such as `wpx-card` or
  `wpx-button`.
- Do not use `@apply` to hide collections of utilities behind CSS classes.
- Keep the CSS entry point limited to Tailwind imports, source discovery, design
  tokens, and rare global rules that cannot be expressed cleanly in markup.
- Extract repeated server-side template fragments when reuse improves behavior
  or accessibility, but keep Tailwind utilities visible on those fragments.
- Use neutral surfaces, consistent borders and spacing, and clear focus states.
  Do not add decorative gradients. A site's tools belong in its own sections;
  shared server settings belong in server navigation.

Production serves the compiled, embedded stylesheet. Node.js and the Tailwind
CLI are build dependencies only and are never installed on a managed server.

## Product copy

Show task names, relevant values, action labels, validation errors, and useful
outcome feedback. Keep identity derivation, privilege boundaries, protocols and
implementation explanations in operator/developer documentation. A destructive
confirmation should name what will be removed, without repeating architecture
on every row. Short actions show success only after completion is confirmed;
long-running work retains its background activity state.
