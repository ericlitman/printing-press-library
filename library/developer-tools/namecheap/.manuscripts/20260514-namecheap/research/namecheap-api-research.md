# Namecheap API research

Source docs: https://www.namecheap.com/support/api/intro/

Namecheap exposes an XML API through a single endpoint, `/xml.response`. Operations are selected with the `Command` query parameter (for example `namecheap.users.getBalances` and `namecheap.domains.check`). Authentication is also query-string based: `ApiUser`, `ApiKey`, `UserName`, and `ClientIp`.

The printed CLI uses a curated OpenAPI 3 spec with command-shaped pseudo paths so the generator can emit distinct Cobra commands. The Namecheap-specific client patch normalizes those pseudo paths back to `/xml.response`, injects the required Namecheap query auth parameters from `NAMECHEAP_USERNAME`, `NAMECHEAP_API_KEY`, and optional `NAMECHEAP_CLIENT_IP`, and converts XML API envelopes into JSON for agent use.

Live read-only validation covered:

- `doctor --json --no-cache`
- `xml-response users-get-balances --agent --no-cache`
- `xml-response domains-check --domain-list example.com --agent --no-cache`
