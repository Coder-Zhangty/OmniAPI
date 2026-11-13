package common

// Matches the current public ingress limit. It is a transport safeguard,
// never evidence that every supplier/model supports this much context.
const OmniModelRequestBodyBytes int64 = 32 << 20
