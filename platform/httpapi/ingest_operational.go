package httpapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/trustvian/trustvian/event"

	platform "trustvian-platform"
)

// operationalFactsFrom reads task 087's optional envelope fields.
//
// Each present field must be canonical decimal text, and the status code must
// be 100-599; anything else is refused rather than read as absent, because an
// adapter following the contract omits a value it could not read. A total
// beside either part is refused too: the adapter reports one only when both
// parts are absent.
func operationalFactsFrom(envelope ingestEnvelope) (platform.OperationalFacts, error) {
	var facts platform.OperationalFacts
	if envelope.HTTPStatusCode != nil {
		code, err := envelopeCount("http_status_code", *envelope.HTTPStatusCode)
		if err != nil {
			return facts, err
		}
		if code < 100 || code > 599 {
			return facts, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: "http_status_code must be from 100 to 599"}
		}
		facts.HTTPStatusCode = uint16(code)
	}
	var u event.Usage
	for _, part := range []struct {
		name  string
		raw   *string
		value *uint64
		has   *bool
	}{
		{"tokens_input", envelope.TokensInput, &u.Input, &u.HasInput},
		{"tokens_output", envelope.TokensOutput, &u.Output, &u.HasOutput},
		{"tokens_unsplit", envelope.TokensUnsplit, &u.Unsplit, &u.HasUnsplit},
	} {
		if part.raw == nil {
			continue
		}
		n, err := envelopeCount(part.name, *part.raw)
		if err != nil {
			return facts, err
		}
		*part.value, *part.has = n, true
	}
	if u.HasUnsplit && (u.HasInput || u.HasOutput) {
		return facts, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "tokens_unsplit is sent only when tokens_input and tokens_output are absent"}
	}
	facts.Usage = u
	return facts, nil
}

// envelopeCount reads one canonical decimal count from the ingest envelope.
func envelopeCount(name, raw string) (uint64, error) {
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(v, 10) != raw {
		return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: fmt.Sprintf("%s must be a canonical decimal string", name)}
	}
	return v, nil
}
