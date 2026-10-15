package policy

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// cRaw is c-raw's volatile.idmap.current in the lab: uid and gid 1000
// mapped to themselves around the default range.
const cRaw = `[{"Isuid":true,"Isgid":false,"Hostid":1000000,"Nsid":0,"Maprange":1000},` +
	`{"Isuid":true,"Isgid":false,"Hostid":1000,"Nsid":1000,"Maprange":1},` +
	`{"Isuid":true,"Isgid":false,"Hostid":1001001,"Nsid":1001,"Maprange":999998999},` +
	`{"Isuid":false,"Isgid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000}]`

func TestTranslateUID(t *testing.T) {
	for name, tt := range map[string]struct {
		raw    string
		uid    uint32
		want   uint32
		reason Reason
	}{
		"single entry, root":      {`[{"Isuid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}]`, 0, 1000000, ""},
		"single entry, uid 1000":  {`[{"Isuid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000000000}]`, 1000, 1001000, ""},
		"raw idmap, root":         {cRaw, 0, 1000000, ""},
		"raw idmap, mapped 1000":  {cRaw, 1000, 1000, ""},
		"raw idmap, above":        {cRaw, 1001, 1001001, ""},
		"gid entries ignored":     {`[{"Isuid":false,"Isgid":true,"Hostid":5,"Nsid":0,"Maprange":10}]`, 0, 0, NoEntry},
		"no entry":                {`[{"Isuid":true,"Hostid":1000000,"Nsid":0,"Maprange":1000}]`, 5000, 0, NoEntry},
		"empty map":               {`[]`, 0, 0, NoEntry},
		"missing":                 {``, 0, 0, Missing},
		"malformed":               {`[{"Isuid":true,`, 0, 0, Unparseable},
		"not a list":              {`{"Isuid":true}`, 0, 0, Unparseable},
		"overlapping entries":     {`[{"Isuid":true,"Hostid":100,"Nsid":0,"Maprange":10},{"Isuid":true,"Hostid":900,"Nsid":5,"Maprange":10}]`, 7, 0, Overlap},
		"overlapping host ranges": {`[{"Isuid":true,"Hostid":100,"Nsid":0,"Maprange":10},{"Isuid":true,"Hostid":105,"Nsid":50,"Maprange":10}]`, 0, 0, Overlap},
		"host uid beyond 32 bits": {`[{"Isuid":true,"Hostid":4294967290,"Nsid":0,"Maprange":100}]`, 0, 0, OutOfRange},
		"negative":                {`[{"Isuid":true,"Hostid":-1,"Nsid":0,"Maprange":10}]`, 0, 0, OutOfRange},
		"empty range":             {`[{"Isuid":true,"Hostid":10,"Nsid":0,"Maprange":0}]`, 0, 0, OutOfRange},
	} {
		entries, err := ParseIDMap(tt.raw)
		var got uint32
		if err == nil {
			got, err = TranslateUID(entries, tt.uid)
		}
		var r *Refusal
		switch {
		case tt.reason == "" && (err != nil || got != tt.want):
			t.Errorf("%s: %d, %v; want %d", name, got, err, tt.want)
		case tt.reason != "" && (!errors.As(err, &r) || r.Reason != tt.reason):
			t.Errorf("%s: %d, %v; want refusal %q", name, got, err, tt.reason)
		}
	}
}

// TestTranslateInverse is PRT-01's property: translating a uid and mapping
// the host uid back gives the original.
func TestTranslateInverse(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 1000 {
		// up to four ranges, non-overlapping on both sides, as a valid map
		// is; the host ranges in a shuffled order
		var entries []IDMapEntry
		n := 1 + r.IntN(4)
		hosts := r.Perm(n)
		ns := int64(r.IntN(100))
		for i := range n {
			size := int64(1 + r.IntN(100000))
			host := int64(hosts[i])*200000 + int64(r.IntN(1000))
			entries = append(entries, IDMapEntry{Isuid: true, Nsid: ns, Hostid: host, Maprange: size})
			ns += size + int64(r.IntN(10))
		}
		e := entries[r.IntN(len(entries))]
		uid := uint32(e.Nsid + int64(r.IntN(int(e.Maprange))))
		host, err := TranslateUID(entries, uid)
		if err != nil {
			t.Fatalf("translate %d in %+v: %v", uid, entries, err)
		}
		back, ok := inverse(entries, host)
		if !ok || back != uid {
			t.Fatalf("uid %d → host %d → %d, %v (%+v)", uid, host, back, ok, entries)
		}
	}
}

// inverse maps a host uid back through the entry translation used, as the
// kernel would for the namespace.
func inverse(entries []IDMapEntry, host uint32) (uint32, bool) {
	for _, e := range entries {
		if e.Isuid && int64(host) >= e.Hostid && int64(host) < e.Hostid+e.Maprange {
			return uint32(e.Nsid + int64(host) - e.Hostid), true
		}
	}
	return 0, false
}

func TestParseProcIDMap(t *testing.T) {
	got, err := ParseProcIDMap("         0    1000000       1000\n      1000       1000          1\n")
	if err != nil || len(got) != 2 || got[1] != (IDMapEntry{Isuid: true, Nsid: 1000, Hostid: 1000, Maprange: 1}) {
		t.Errorf("ParseProcIDMap = %+v, %v", got, err)
	}
	if _, err := ParseProcIDMap("0 1000000"); err == nil {
		t.Error("a short line was accepted")
	}
}

func TestCompareUIDMaps(t *testing.T) {
	lxd, _ := ParseIDMap(cRaw)
	proc, _ := ParseProcIDMap("0 1000000 1000\n1000 1000 1\n1001 1001001 999998999\n")
	if ok, diff := CompareUIDMaps(lxd, proc); !ok {
		t.Errorf("c-raw's maps differ: %s", diff)
	}
	other, _ := ParseProcIDMap("0 1065536 65536\n")
	if ok, diff := CompareUIDMaps(lxd, other); ok || diff == "" {
		t.Errorf("different maps match: %v %q", ok, diff)
	}
}
