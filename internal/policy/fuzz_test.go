package policy

import "testing"

// FuzzParseIDMap: volatile.idmap.current comes from LXD but is parsed
// defensively (SEC-11, T18): no panic, and an accepted translation of uid 0
// comes from exactly one uid entry that covers it.
func FuzzParseIDMap(f *testing.F) {
	f.Add(cRaw)
	f.Add(`[{"Isuid":true,"Isgid":false,"Hostid":1065536,"Nsid":0,"Maprange":65536}]`)
	f.Add(`[]`)
	f.Add(`{`)
	f.Fuzz(func(t *testing.T, raw string) {
		entries, err := ParseIDMap(raw)
		if err != nil {
			return
		}
		host, err := TranslateUID(entries, 0)
		if err != nil {
			return
		}
		covering := 0
		for _, e := range entries {
			if e.Isuid && e.Nsid <= 0 && 0 < e.Nsid+e.Maprange {
				covering++
				if e.Hostid-e.Nsid != int64(host) {
					t.Fatalf("uid 0 -> %d, entry %+v", host, e)
				}
			}
		}
		if covering != 1 {
			t.Fatalf("%d entries cover uid 0 in %s", covering, raw)
		}
	})
}

// FuzzParseProcIDMap: a guest's /proc/self/uid_map, read by lxd-probe.
func FuzzParseProcIDMap(f *testing.F) {
	f.Add("         0    1000000 1000000000\n")
	f.Add("0 0 4294967295\n")
	f.Add("x y z\n")
	f.Fuzz(func(t *testing.T, text string) {
		_, _ = ParseProcIDMap(text)
	})
}
