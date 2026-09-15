# Outbound webhook signing

Dydan signs every outbound webhook it generates (circle activity webhooks
and notification webhooks) so receivers can verify **authenticity**,
**integrity** and **freshness** of each delivery.

## Headers

Every delivery carries:

```
X-Dydan-Signature: t=<unix-seconds>,v1=<hex>
X-Dydan-Timestamp: <unix-seconds>
Content-Type: application/json
```

`X-Daydan-Signature` and `X-Daydan-Timestamp` are emitted with identical
values for existing receivers. New integrations must use the Dydan headers.

## Canonical signing input

The HMAC covers exactly two things joined by a single dot:

```
<timestamp-decimal> + "." + <exact raw request body>
```

`signature = hex(HMAC-SHA256(secret, canonical_input))`

The body is signed as the exact bytes transmitted. Never re-marshal or
re-indent JSON before verifying; whitespace differences change the signature
by design.

## Verification procedure (receivers)

1. Read the raw request body into memory.
2. Parse `t` from `X-Dydan-Signature` (or `X-Dydan-Timestamp`). Existing
   receivers may continue using the legacy `X-Daydan-*` headers.
3. Reject the delivery if `|now - t| > tolerance` (we recommend 5 minutes).
4. Compute `HMAC-SHA256(shared_secret, t + "." + raw_body)`.
5. Compare against `v1` using a **constant-time** comparison
   (`hmac.Equal` in Go, `hmac.compare_digest` in Python). Never use `==`.
6. Only then parse/handle the payload.

Reference Go implementation:

```go
func verify(secret string, tolerance time.Duration, body []byte,
    signatureHeader string, now time.Time) bool {
    i := strings.Index(signatureHeader, ",")
    ts, err := strconv.ParseInt(strings.TrimPrefix(signatureHeader[:i], "t="), 10, 64)
    if err != nil || time.Unix(ts, 0).Before(now.Add(-tolerance)) {
        return false
    }
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(append([]byte(strconv.FormatInt(ts, 10)+"."), body...))
    want := mac.Sum(nil)
    got, _ := hex.DecodeString(strings.TrimPrefix(signatureHeader[i+1:], "v1="))
    return hmac.Equal(want, got)
}
```

## Secret lifecycle

* The key is an independent credential family. It is **not** the JWT secret,
  the media-signing secret, or any provider secret.
* Configuration precedence:
   1. `webhook.signing_secret` in config (or `DYDAN_WEBHOOK_SIGNING_SECRET`;
      `DAYDAN_WEBHOOK_SIGNING_SECRET` and older prefixes remain aliases);
  2. `webhook.signing_secret_file`, auto-created with 32 random bytes
     (0600) on first boot at `<data_dir>/webhook-signing-secret`;
* Rotation: set a new secret (or replace the secret file) and restart.
  Signatures made with the old key stop verifying once receivers are given
  the new key. There is deliberately no unsigned fallback mode: a receiver
  that does not verify signatures has no authenticity guarantee.

## Delivery semantics

Delivery is at-least-once per event while the server process lives:

* `2xx` → considered delivered.
* `408`, `429`, `5xx`, network/timeouts → retried up to
  `webhook.max_attempts` (default 4 total attempts) with exponential
  backoff (+/-20% jitter) capped at `webhook.backoff_max` (default 30s);
  a sane `Retry-After` header is honored and clamped to that cap.
* Other `4xx` → permanent rejection, no retry.
* After the final attempt the event is dropped (logged with attempt count).
* Queue overflow drops the newest events (per-tenant and global bounds).

There is **no exactly-once guarantee**: design receivers idempotently.
