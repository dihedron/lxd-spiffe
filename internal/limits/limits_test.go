package limits

import "testing"

// TestSEC11Bounds pins the bounds of SEC-11, FR-S2, FR-A3 and FR-S17 (6),
// so that a change to any of them is deliberate.
func TestSEC11Bounds(t *testing.T) {
	for name, tt := range map[string]struct{ got, want int }{
		"payload":         {MaxPayload, 4096},
		"string":          {MaxString, 128},
		"name":            {MaxName, 63},
		"nonce":           {NonceSize, 32},
		"hex proof":       {HexProofSize, 64},
		"LXD response":    {MaxLXDResponse, 1 << 20},
		"files response":  {MaxFilesResponse, 4096},
		"devLXD response": {MaxDevLXDResponse, 64 << 10},
		"proof modes":     {MaxProofModes, 2},
	} {
		if tt.got != tt.want {
			t.Errorf("%s: %d, want %d", name, tt.got, tt.want)
		}
	}
}
