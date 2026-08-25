package events

// Header names for outbound webhook authenticity. Kept here so both the
// signer and the delivery path reference a single definition.
const (
	signatureHeaderName = "X-Daydan-Signature"
	timestampHeaderName = "X-Daydan-Timestamp"
)
