# Feature module boundaries

WPX keeps feature-specific request types, validation, storage, broker operations,
host reconciliation, and tests together. Shared route registration, migrations,
worker dispatch and process construction connect those modules. Narrow optional
broker interfaces avoid granting unrelated authority to an operation.

- [Files](files.md) and [filesystem confinement](files_manager.md)
- [Database interface](database-ui.md)
- [Site navigation](navigation.md) and [short actions](quick-actions.md)
- [WordPress defenses](security-defense.md)
- [Operator alerts](operator-alerts.md)
- [WordPress fleet](fleet.md)
- [Cron](cron.md) and [site access](site-access.md)
- [Node, FTP and outbound mail](hosting-services.md)
- [Localization and cloud discovery](localization-cloud.md)
- [Integration verification](VERIFICATION.md)

All privileged operations validate typed inputs at the broker boundary. Site
mutations preserve assignment checks, ownership, lifecycle reservations and
idempotent recovery. The existing SQLite migration history remains unchanged;
new feature migrations append in deterministic order.

The interface uses Go templates and direct Tailwind utilities. Product copy
contains labels, values, actionable errors and necessary field constraints.
Implementation and recovery explanations belong in these documents.
