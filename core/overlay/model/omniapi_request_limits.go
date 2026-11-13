package model

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

func omniValidateRequestLimits(rows map[string]OmniRequestLimits) error {
	if len(rows) > 16 {
		return errors.New("too many request limit paths")
	}
	for path, row := range rows {
		if !strings.HasPrefix(path, "/v1/") || len(path) > 120 || strings.ContainsAny(path, "?#\r\n") {
			return errors.New("invalid request limit path")
		}
		switch row.Basis {
		case "platform_configuration", "accepted_parameters", "supplier_documented", "boundary_verified":
		default:
			return errors.New("invalid request limit evidence basis")
		}
		if len(row.EvidenceHash) != 64 || row.VerifiedAt <= 0 || row.VerifiedAt > time.Now().Unix()+300 {
			return errors.New("request limits need dated evidence")
		}
		for _, value := range []int64{row.MaxInputTokens, row.MaxContextTokens, row.MaxOutputTokens, row.MaxRequestBytes, row.MaxMediaBytes, row.InputOverheadTokens} {
			if value < 0 || value > 1<<30 {
				return errors.New("invalid finite request limit")
			}
		}
		if row.MaxInputTokens == 0 && row.MaxContextTokens == 0 && row.MaxOutputTokens == 0 && row.MaxRequestBytes == 0 && row.MaxMediaBytes == 0 {
			return errors.New("empty request limit evidence")
		}
		if row.MaxContextTokens > 0 && (row.MaxInputTokens > row.MaxContextTokens || row.MaxOutputTokens > row.MaxContextTokens) {
			return errors.New("input/output limit exceeds total context")
		}
		if (row.Basis == "accepted_parameters" || row.Basis == "platform_configuration") && row.FullOutputVerified {
			return errors.New("accepted output parameter is not full generation proof")
		}
		if row.Basis == "supplier_documented" && len(row.Sources) == 0 {
			return errors.New("documented limits require supplier source")
		}
		if len(row.Sources) > 8 {
			return errors.New("too many request limit sources")
		}
		for _, source := range row.Sources {
			u, e := url.Parse(source)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(source) > 512 {
				return errors.New("invalid public request limit source")
			}
		}
	}
	return nil
}

func (b OmniModelBinding) LimitsFor(path string) (OmniRequestLimits, bool) {
	row, ok := b.RequestLimits[path]
	if !ok || omniValidateRequestLimits(map[string]OmniRequestLimits{path: row}) != nil {
		return OmniRequestLimits{}, false
	}
	return row, true
}

// Accepted parameters establish a usable default, not a physical maximum.
func (b OmniModelBinding) HardLimitsFor(path string) (OmniRequestLimits, bool) {
	row, ok := b.LimitsFor(path)
	return row, ok && (row.Basis == "supplier_documented" || row.Basis == "boundary_verified")
}

// Admission limits may be a conservative local envelope, distinct from a
// documented upstream maximum. A parameter acceptance is not a hard-cap claim.
func (b OmniModelBinding) AdmissionLimitsFor(path string) (OmniRequestLimits, bool) {
	if !b.VerifiedPathsOnly {
		return b.HardLimitsFor(path)
	}
	row, ok := b.LimitsFor(path)
	if ok && row.Basis == "platform_configuration" {
		return OmniRequestLimits{}, false
	}
	return row, ok
}

func (b OmniModelBinding) RejectEnvelope(path string, input, output, bodyBytes int64) *OmniBindingRejection {
	row, ok := b.AdmissionLimitsFor(path)
	if !ok {
		return nil
	}
	if input < 0 || output < 0 || bodyBytes < 0 || input > int64(^uint64(0)>>1)-row.InputOverheadTokens {
		return &OmniBindingRejection{Reason: "request_envelope_invalid"}
	}
	input += row.InputOverheadTokens
	reject := func(reason string, limit int64) *OmniBindingRejection {
		return &OmniBindingRejection{Reason: reason, Limit: int(limit), InputTokens: input, OutputTokens: output, KnownLimit: true}
	}
	if row.MaxInputTokens > 0 && input > row.MaxInputTokens {
		return reject("input_limit_exceeded", row.MaxInputTokens)
	}
	if row.MaxOutputTokens > 0 && output > row.MaxOutputTokens {
		return reject("output_limit_exceeded", row.MaxOutputTokens)
	}
	// Subtract instead of summing client-controlled values to avoid overflow.
	if row.MaxContextTokens > 0 && (input > row.MaxContextTokens || output > row.MaxContextTokens-input) {
		return reject("context_length_exceeded", row.MaxContextTokens)
	}
	if row.MaxRequestBytes > 0 && bodyBytes > row.MaxRequestBytes {
		return reject("request_body_too_large", row.MaxRequestBytes)
	}
	return nil
}

// Older registries store a single output number even when Chat and Responses
// differ. The approved capability supplement already has exact path evidence;
// read that before falling back to a legacy protocol-wide default. This does
// not change purchase snapshots or certify physical generation maxima.
func (b OmniModelBinding) AcceptedOutputForPath(name, path string) (int64, bool) {
	if row, ok := b.LimitsFor(path); ok && row.MaxOutputTokens > 0 {
		return row.MaxOutputTokens, true
	}
	if release := omniCapabilityRelease(); release != nil {
		if bound, err := release.Profile.Capability(name, b.ChannelID, path); err == nil {
			return bound.MaxOutputTokens, true
		}
	}
	return 0, false
}
