package usage

import coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"

// normalizeModelObservation validates captured facts without filling historical
// gaps from aliases or pricing models. These fields never change billing keys.
func normalizeModelObservation(detail *RequestDetail) {
	detail.RequestedModel = coreusage.NormalizeObservedModel(detail.RequestedModel)
	detail.UpstreamModel = coreusage.NormalizeObservedModel(detail.UpstreamModel)
	detail.UpstreamResponseModel = coreusage.NormalizeObservedModel(detail.UpstreamResponseModel)
	switch detail.UpstreamResponseModelSource {
	case "header", "body", "metadata":
	default:
		detail.UpstreamResponseModelSource = ""
	}
	if detail.UpstreamResponseModel == "" {
		detail.UpstreamResponseModelSource = ""
	}
}

// modelMatch compares only the wire request and the upstream's reported model.
// Client aliases and price-table matches are independent of this observation.
func modelMatch(detail RequestDetail) string {
	sent := coreusage.NormalizeObservedModel(detail.UpstreamModel)
	returned := coreusage.NormalizeObservedModel(detail.UpstreamResponseModel)
	if sent == "" || returned == "" {
		return "unknown"
	}
	if sent == returned {
		return "matched"
	}
	return "mismatch"
}
