# Retired local attended API

Brine removed the disabled local Alpha preview and confirmation API. The former local preview endpoint is no longer routed. Normal local stack actions and the separate HTTPS remote deployment flow are unchanged. Existing secret state and downgrade protections must not be removed or bypassed.

The [historical design](https://github.com/umayr/brine/blob/4a805c56f12a03549094b54c39b97fd08e4fbfde/docs/alpha-attended-flow.md) and [paired canary notes](alpha-paired-app-canary-receiving.md) describe the old contract and synthetic test evidence. That evidence did not qualify native execution and is not an operating guide.
