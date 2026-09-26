# User data export format

`GET /api/v1/users/export` returns the portable JSON document
`dydan-user-export/v1` as a private, non-cacheable download for the
authenticated account.

The document contains a projected account profile, user preferences, and the
`data` object: tasks and task history the account is authorized to view in its
active circle; the account's things and thing history; meters created by the
account and their readings; and the account's inbox notifications. Every query
is constrained by both the authenticated user and active circle.

It never includes password hashes, sessions or refresh tokens, MFA secrets or
recovery material, API-token values, SMTP/provider configuration, signing or
encryption keys, notification delivery targets, or raw provider payloads.

The Settings restore action intentionally restores only the validated
`preferences` portion to the matching authenticated account. It does not
recreate task, meter, thing, or notification records.
