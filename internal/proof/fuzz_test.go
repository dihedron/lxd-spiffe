package proof

import "testing"

// FuzzComputeInjective: the length prefixes make the encoding injective, so
// two different (project, instance) pairs never give the same proof; this
// is what the vectors ("ab","c") and ("a","bc") check for one case.
func FuzzComputeInjective(f *testing.F) {
	f.Add("ab", "c", "a", "bc")
	f.Add("", "c1", "c1", "")
	f.Add("default", "c1", "default", "c1")
	f.Fuzz(func(t *testing.T, p1, n1, p2, n2 string) {
		a, err1 := Compute(p1, n1, serverNonce, agentNonce)
		b, err2 := Compute(p2, n2, serverNonce, agentNonce)
		if err1 != nil || err2 != nil {
			return
		}
		if (a == b) != (p1 == p2 && n1 == n2) {
			t.Fatalf("(%q,%q) and (%q,%q): equal proofs %v", p1, n1, p2, n2, a == b)
		}
	})
}
