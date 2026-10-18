package tuning

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Knob is one turnable value (see the var block in tuning.go).
type Knob struct {
	Name  string
	Seed  bool // shapes the bundle: needs a reseed
	ptrI  *int
	ptrF  *float64
	ptrSW string // SourceWeight key ("SourceWeight.docs")
}

func knobList() []Knob {
	ki := func(n string, p *int, seed bool) Knob { return Knob{Name: n, ptrI: p, Seed: seed} }
	kf := func(n string, p *float64) Knob { return Knob{Name: n, ptrF: p} }
	ks := []Knob{
		ki("Candidates", &Candidates, false), ki("TopK", &TopK, false), ki("RerankTopK", &RerankTopK, false),
		kf("RRFK", &RRFK), ki("PerPageCap", &PerPageCap, false), kf("NearDupJaccard", &NearDupJaccard),
		kf("TitleWeight", &TitleWeight), kf("BM25K1", &BM25K1), kf("BM25B", &BM25B),
		kf("SynonymWeight", &SynonymWeight), ki("MaxQueryTerms", &MaxQueryTerms, false),
		kf("ProductBoost", &ProductBoost), kf("DeprecatedPenalty", &DeprecatedPenalty), kf("LowValuePenalty", &LowValuePenalty),
		kf("FloorCoverage", &FloorCoverage), kf("FloorCosine", &FloorCosine), kf("WeakCoverage", &WeakCoverage),
		ki("ContextCharsPerChunk", &ContextCharsPerChunk, false),
		ki("ChunkMaxWords", &ChunkMaxWords, true), ki("ChunkOverlapWords", &ChunkOverlapWords, true),
		ki("ChunkMinWords", &ChunkMinWords, true), ki("ChunkMergeWords", &ChunkMergeWords, true),
		ki("PieceWords", &PieceWords, true), ki("PieceOverlapWords", &PieceOverlapWords, true),
		ki("ExternalMaxChunks", &ExternalMaxChunks, true),
	}
	var sw []string
	for k := range SourceWeight {
		sw = append(sw, string(k))
	}
	sort.Strings(sw)
	for _, k := range sw {
		ks = append(ks, Knob{Name: "SourceWeight." + k, ptrSW: k})
	}
	sort.SliceStable(ks, func(i, j int) bool { return ks[i].Seed == false && ks[j].Seed })
	return ks
}

// Knobs lists every knob with its current value (query-time first).
func Knobs() []struct {
	Name, Value string
	Seed        bool
} {
	var out []struct {
		Name, Value string
		Seed        bool
	}
	for _, k := range knobList() {
		out = append(out, struct {
			Name, Value string
			Seed        bool
		}{k.Name, k.get(), k.Seed})
	}
	return out
}

func (k Knob) get() string {
	switch {
	case k.ptrI != nil:
		return strconv.Itoa(*k.ptrI)
	case k.ptrF != nil:
		return strconv.FormatFloat(*k.ptrF, 'g', -1, 64)
	default:
		for sk, v := range SourceWeight {
			if string(sk) == k.ptrSW {
				return strconv.FormatFloat(v, 'g', -1, 64)
			}
		}
	}
	return ""
}

// Set overrides one knob by name. For the test harness only.
func Set(name, value string) error {
	for _, k := range knobList() {
		if !strings.EqualFold(k.Name, name) {
			continue
		}
		switch {
		case k.ptrI != nil:
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("knob %s: %q is not an integer", k.Name, value)
			}
			*k.ptrI = n
		case k.ptrF != nil:
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("knob %s: %q is not a number", k.Name, value)
			}
			*k.ptrF = f
		default:
			f, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return fmt.Errorf("knob %s: %q is not a number", k.Name, value)
			}
			for sk := range SourceWeight {
				if string(sk) == k.ptrSW {
					SourceWeight[sk] = f
				}
			}
		}
		return nil
	}
	return fmt.Errorf("unknown knob %q", name)
}

// IsSeedKnob reports whether the named knob needs a reseed.
func IsSeedKnob(name string) bool {
	for _, k := range knobList() {
		if strings.EqualFold(k.Name, name) {
			return k.Seed
		}
	}
	return false
}
