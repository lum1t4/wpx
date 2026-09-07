# Database interface redesign

The server database page and each site's Databases tool use the same interaction model. Application databases appear as compact rows with a readable status, generated database name, and actions available only when the database is active. WordPress databases remain a separate list because their lifecycle follows the WordPress site.

Database creation is a visible form reached from the page's primary action. The server page requires an active site and a label; the site page already has the site context and asks only for a label. Creation and deletion forms opt into the shared quick-action response with `data-quick-action`, while ordinary form submissions retain their POST/redirect/GET behavior. The database handlers pass the real queued job ID to `respondQueuedAction` so enhanced requests report worker state rather than assuming success. phpMyAdmin installation is deliberately excluded because package installation is a longer background operation.

Credential reveal remains an authenticated POST. The response places host, database name, username, and password in labeled read-only fields and provides a clear link back to the unselected page. Active rows show the phpMyAdmin action when the console is installed. When it is unavailable, site pages direct server administrators to the server database page and give collaborators a concise request for administrator action.

The interface no longer explains UUID-derived identifiers, site-association authorization, or SQL privilege boundaries. Those invariants remain documented in [Architecture](../ARCHITECTURE.md#stable-site-identity) and the operational behavior remains documented in [Operations](../OPERATIONS.md#databases-and-phpmyadmin). The interface retains user-facing guidance that affects a decision, including the need to save revealed credentials and back up application databases separately.

No shared route, render, navigation, base template, stylesheet, or page-data changes are required.
