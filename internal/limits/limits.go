// Package limits holds the bounds on untrusted input (attestor spec SEC-11,
// FR-S2, FR-A3, FR-S17 (6)) in one place, so that the agent, the server and
// the LXD clients enforce the same values.
package limits

const (
	// MaxPayload bounds the attestation payload and every protocol message.
	MaxPayload = 4096
	// MaxString bounds every string field of a protocol message.
	MaxString = 128
	// MaxName bounds a project or instance name, as LXD accepts them.
	MaxName = 63
	// NonceSize is the exact size of the agent and server nonces (SEC-03).
	NonceSize = 32
	// HexProofSize is the exact size of a proof in lowercase hex: the proof
	// file of file_pull and the echo of config_push.
	HexProofSize = 64
	// MaxLXDResponse bounds a JSON response of the LXD API.
	MaxLXDResponse = 1 << 20
	// MaxFilesResponse bounds a response of the LXD files API.
	MaxFilesResponse = 4096
	// MaxDevLXDResponse bounds a response of devLXD (FR-A3).
	MaxDevLXDResponse = 64 << 10
	// MaxProofModes bounds the proof modes an agent offers.
	MaxProofModes = 2
)
