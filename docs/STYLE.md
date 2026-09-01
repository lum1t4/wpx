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

## Compatibility

The public HTTP API and broker protocol are versioned. State migrations are
forward-only during normal operation, but every release documents and tests the
rollback strategy. Generated configuration contains a WPX ownership marker and
is never allowed to overwrite an unmanaged file silently.

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

Production serves the compiled, embedded stylesheet. Node.js and the Tailwind
CLI are build dependencies only and are never installed on a managed server.
