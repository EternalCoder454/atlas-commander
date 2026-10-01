# Phone API (v1)

The contract between Commander on the PC (`internal/remote`) and the Android app
(`android/`). Both sides are built from this file; change it first, then both.

## Model

The phone is a remote control. Agents always run on the PC, and the phone only
sees and steers them while Commander is running there. Phone access is off until
the user turns it on in Settings → Phone.

## Transport and trust

- HTTPS on one TCP port (default 47821) on every interface of the PC.
- Commander makes a self-signed ECDSA P-256 certificate once and keeps it in its
  data dir (`phone/cert.pem`, `phone/key.pem`, mode 0600), valid 10 years.
- The phone does not use the system trust store. It pins the certificate: the
  lowercase hex SHA-256 of the leaf certificate's DER bytes must equal the
  fingerprint from pairing. Host names are not checked; the pin is the check.
- Every request carries `Authorization: Bearer <token>`. The token is 32 random
  bytes, base64url without padding, kept in the data dir (`phone/token`, 0600),
  and compared in constant time. "Forget paired phones" makes a new token.
- After 10 failed tokens from one address within a minute, that address gets
  429 for a minute.

## Pairing link

Commander shows this link as a QR code and as copyable text:

    atlascommander://pair?v=1&n=<PC name>&h=<host:port>,<host:port>&t=<token>&f=<fingerprint>

- `h` lists every reachable address. LAN IPv4 comes first, then other
  addresses such as Tailscale's 100.x. The phone tries them in order and
  remembers the first that answers.
- All values are URL-encoded.

## Requests

All bodies are JSON (`Content-Type: application/json`). Times are RFC 3339 UTC,
or null. Money is USD as a float.

### `GET /api/v1/ping`

The phone calls this to check a pairing. Response:

    {"name": "zach-pc", "version": "0.1.0", "demo": false}

### `GET /api/v1/state`

Response:

    {
      "seq": 812,
      "name": "zach-pc",
      "version": "0.1.0",
      "demo": false,
      "spent_usd": 1.84,
      "fleets": [
        {"id": "f1", "name": "Website", "budget_usd": 20, "spent_usd": 3.1,
         "agents": 3, "live": 1}
      ],
      "agents": [
        {"id": "a1", "name": "Builder", "fleet_id": "f1", "fleet_name": "Website",
         "provider": "claude-code", "model": "sonnet",
         "status": "running",
         "task": "Fix the login form",
         "last_tool": "Bash: go test ./...",
         "session_cost_usd": 0.42, "cost_usd": 2.9, "cap_usd": 5,
         "error": "", "pending": 1, "started_at": "2026-10-01T07:00:00Z"}
      ],
      "approvals": [
        {"id": "p9", "agent_id": "a1", "agent_name": "Builder", "tool": "Bash",
         "summary": "rm -rf build", "input": "{\n  \"command\": \"rm -rf build\"\n}",
         "at": "2026-10-01T07:03:10Z"}
      ]
    }

- `seq` increases on every change.
- `status` is one of: idle, starting, running, waiting, approval, held, capped,
  error, stopped.
- `provider` is one of: claude-code, openai, gemini, local.
- `cap_usd` and `budget_usd` are 0 when unset.
- Archived agents are left out.

### `GET /api/v1/agents/{id}/transcript?from=N`

Returns the entries from index N on (start at 0) and the index to ask for next:

    {"entries": [{"at": "...", "kind": "text", "text": "...", "tool": "",
                  "is_error": false, "user": false}], "next": 57}

- `kind` is one of: text, tool, result, error, info.
- `user` is true for text the user sent.

### Actions

The success response for all of these is `{"ok": true}`, or for kill-all
`{"ok": true, "killed": 2}`:

- `POST /api/v1/agents/{id}/start` with `{"prompt": "..."}` starts a session.
- `POST /api/v1/agents/{id}/send` with `{"text": "..."}` re-prompts a waiting
  agent, or redirects a running one.
- `POST /api/v1/agents/{id}/hold`, `/resume`, `/stop` and `/kill` take no body.
- `POST /api/v1/approvals/{id}` with `{"allow": true, "reason": ""}` decides an
  approval.
- `POST /api/v1/kill-all` takes no body.

## Errors

Errors are `{"error": "<short plain sentence for the user>"}` with one of these
statuses:

| Status | Meaning |
|---|---|
| 400 | Bad body |
| 401 | Bad or old token. Message: "This phone isn't paired any more. Pair it again from Commander's Settings." |
| 404 | Unknown agent or approval |
| 409 | Not possible in this state |
| 429 | Too many failed tokens |
| 500 | Anything else |

The phone shows the `error` text as it is.
