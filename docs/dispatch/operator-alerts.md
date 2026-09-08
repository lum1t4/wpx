# Operator notifications

Notifications run in the unprivileged panel process. Configure them under
**Alerts**. The master switch pauses delivery; each channel has its own enable,
configuration and test controls. The same condition switches apply to all
selected channels.

## Delivery channels

- **Email:** SMTP host, port, STARTTLS or TLS, optional authentication, sender and
  recipient. SMTP connections require encryption.
- **Slack:** an [incoming webhook](https://api.slack.com/messaging/webhooks) URL
  from the Slack workspace. Treat the complete URL as a credential.
- **Telegram:** a bot token and destination chat ID. Add the bot to the intended
  chat and allow it to post. WPX uses the official
  [`sendMessage` API](https://core.telegram.org/bots/api#sendmessage).

Use the channel's **Test** button to check delivery without enabling monitoring
or sending to the other channels. A test uses the form values and does not save
settings. Save changes separately. Blank credential fields retain existing
secrets; the page never displays stored SMTP passwords, webhook URLs or bot
tokens. Configuration is encrypted in SQLite with WPX's existing encryption key.

Webhook requests use fixed official provider destinations, HTTPS, bounded
payloads and timeouts, and do not follow redirects. A successful HTTP status must
also carry the provider's success response. Error messages omit secret URLs,
tokens and provider response bodies. No provider account or webhook is configured
by the installer.

## Conditions and delivery stability

CPU, memory, disk, service failures, certificate expiry, WPX updates and OOM
conditions can be selected independently. Resource alerts require sustained bad
samples; recovery requires healthy samples. Cooldown is configurable from
15 minutes to seven days. Diagnostic messages contain bounded resource and
service snapshots, not arbitrary configuration files or credentials.

Delivery state is persisted per incident and channel. One failed destination does
not cause repeated successful delivery to another destination. Recovery and new
incidents are distinct delivery phases. A transport timeout or process crash
between provider acceptance and local persistence can still produce a duplicate:
these provider APIs do not share a transaction with WPX's state database.

Managed swap is opt-in. When absent, WPX can create a protected 2 GiB managed swap
file after the configured sustained-memory condition. Existing swap is preserved.
OOM events and service diagnostics are bounded reads; no arbitrary operator
command is accepted by the privileged broker.

## Verification scope

Automated tests cover encrypted storage, legacy SMTP settings, redaction,
provider request/response contracts, timeouts and redirects, target-specific
tests, partial failure, cooldown and incident recovery. Live Slack, Telegram and
SMTP delivery requires operator-provided credentials and was not exercised by
the development fixtures. Deployment observations are recorded in
[verification](../VERIFICATION.md).
