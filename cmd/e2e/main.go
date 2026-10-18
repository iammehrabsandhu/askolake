// Command e2e records the LLM request each eval/e2e.jsonl case (the twin of
// e2e.md) would send, without calling an LLM, and checks it: outcome
// (llm/refused/dont_know), expected source URL in the passages, every
// must_contain fact present, no must_not_contain match. In-process mode runs
// the real answer chain (~5 s with eval/golden.jsonl); -server runs the real
// binary with DEBUG=on and a dead LLM URL (same requests, ~2 min).
//
// Run: testing/run.sh [flags]. Read testing/runs/<time>-<name>/summary.md
// first; cases.md, requests/NN.txt and golden.md hold the details.
// Instant: -set NAME=VALUE / -knobs file.yaml (list: -dump-knobs) and
// -data <copy of data/> (synonyms, low_value, products, deprecated).
// Needs -reseed (network, ~1 min): seed knobs, sources; -index reuses a bundle.
//
// Limits: -server uses compiled-in settings only; answer-only must-mentions
// ("not supported") need a real LLM; facts match substrings of the
// truncated passages; seed knobs without -reseed are ignored (warned).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"olake-ask/internal/app"
	"olake-ask/internal/config"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/index"
	"olake-ask/internal/seed"
	"olake-ask/internal/tuning"
)

// spec is one case (eval/e2e.jsonl or eval/golden.jsonl).
type spec struct {
	ID          int      `json:"id"`
	Question    string   `json:"question"`
	Outcome     string   `json:"outcome"` // llm | refused | dont_know, "|"-separated alternatives; "" = llm
	Expect      []string `json:"expect_url_contains"`
	MustContain []string `json:"must_contain"`
	MustNot     []string `json:"must_not_contain"` // regexes, case-insensitive
}

// capture is what reached (or would have reached) the LLM for one question.
type capture struct {
	Mode    string // answered | dont_know | search_only | refused
	LLM     bool   // the answer request was made
	System  string
	User    string
	Model   string
	Calls   int // LLM requests made (rewrite/rerank add calls)
	Elapsed time.Duration
}

type passage struct {
	N            int
	Label, Title string
	URL, Text    string
}

type result struct {
	Spec      spec
	Cap       capture
	Passages  []passage
	Outcome   string
	OutcomeOK bool
	SrcRank   int // 1-based rank of the first expected URL, 0 = missing
	Missing   []string
	Forbidden []string
	Pass      bool
}

type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ",") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	var sets setFlags
	specPath := flag.String("spec", "eval/e2e.jsonl", "cases to run (JSONL)")
	mdPath := flag.String("md", "e2e.md", "human expected-output table shown next to each case (\"\" = none)")
	goldenPath := flag.String("golden", "eval/golden.jsonl", "also score these questions with the same settings (\"\" = skip)")
	knobsPath := flag.String("knobs", "", "YAML file of knob overrides (name: value)")
	flag.Var(&sets, "set", "knob override NAME=VALUE (repeatable; applied after -knobs)")
	dataDir := flag.String("data", "", "directory with alternative data/*.yaml (default: compiled-in)")
	indexDir := flag.String("index", "", "bundle directory to load (default: compiled-in assets/index)")
	reseed := flag.Bool("reseed", false, "seed a fresh bundle into the run directory first (network; uses seed-time knobs, -data and EMBEDDER/EMBED_* env)")
	server := flag.Bool("server", false, "run the real server binary with DEBUG=on and a dead LLM URL (compiled-in settings only)")
	name := flag.String("name", "run", "label for the run directory")
	outRoot := flag.String("out", "testing/runs", "where run directories are created")
	dump := flag.Bool("dump-knobs", false, "print every knob with its default as YAML and exit")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	if *dump {
		dumpKnobs(os.Stdout)
		return
	}
	if err := run(runOpts{*specPath, *mdPath, *goldenPath, *knobsPath, sets, *dataDir, *indexDir, *reseed, *server, *name, *outRoot}); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
}

type runOpts struct {
	spec, md, golden, knobs string
	sets                    []string
	data, index             string
	reseed, server          bool
	name, out               string
}

func run(o runOpts) error {
	if o.server && (o.knobs != "" || len(o.sets) > 0 || o.data != "" || o.index != "" || o.reseed) {
		return errors.New("-server uses the compiled-in tuning, data and bundle; drop -knobs/-set/-data/-index/-reseed (or run without -server)")
	}
	overrides, err := applyKnobs(o.knobs, o.sets)
	if err != nil {
		return err
	}
	for _, kv := range overrides {
		if k, _, _ := strings.Cut(kv, "="); tuning.IsSeedKnob(k) && !o.reseed {
			fmt.Fprintf(os.Stderr, "e2e: warning: %s is a seed-time knob; it has no effect without -reseed\n", k)
		}
	}
	cases, err := readJSONL(o.spec)
	if err != nil {
		return err
	}
	rows, err := readMD(o.md)
	if err != nil {
		return err
	}
	dir := filepath.Join(o.out, time.Now().Format("20060102-150405")+"-"+safeName(o.name))
	if err := os.MkdirAll(filepath.Join(dir, "requests"), 0o755); err != nil {
		return err
	}

	d, err := loadData(o.data)
	if err != nil {
		return err
	}
	if o.reseed {
		if o.index, err = reseedInto(d, filepath.Join(dir, "index")); err != nil {
			return err
		}
	}
	b, err := loadBundle(o.index)
	if err != nil {
		return err
	}
	// Product, deprecated and low-value flags follow the data files in use, so
	// -data experiments with low_value/products/deprecated need no reseed.
	seed.Tag(b.Chunks, d)

	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}
	var results []result
	var golden []result
	if o.server {
		caps, err := serverCaptures(dir, questions(cases))
		if err != nil {
			return err
		}
		for _, c := range cases {
			results = append(results, evaluate(c, caps[c.Question]))
		}
		o.golden = "" // too slow through the per-IP rate limit
	} else {
		ans := newCapturer(cfg, d, b)
		for _, c := range cases {
			results = append(results, evaluate(c, ans.capture(c.Question)))
		}
		if o.golden != "" {
			gcases, err := readJSONL(o.golden)
			if err != nil {
				return err
			}
			for _, c := range gcases {
				golden = append(golden, evaluate(c, ans.capture(c.Question)))
			}
		}
	}

	for i, r := range results {
		if err := os.WriteFile(filepath.Join(dir, "requests", fmt.Sprintf("%02d.txt", caseID(r.Spec, i))), []byte(requestText(r.Cap)), 0o644); err != nil {
			return err
		}
	}
	meta := runMeta{Dir: dir, Mode: map[bool]string{true: "server (DEBUG=on, dead LLM URL)", false: "in-process capture"}[o.server],
		Spec: o.spec, Golden: o.golden, Data: orDefault(o.data, "compiled-in data/"), Index: orDefault(o.index, "compiled-in assets/index"),
		Version: b.Manifest.Version, Chunks: len(b.Chunks), Overrides: overrides}
	if e := b.Manifest.Embedder; e != nil {
		meta.Vectors = e.Provider + "/" + e.Model
	}
	summary := writeSummary(meta, results, golden)
	files := map[string]string{
		"summary.md": summary,
		"cases.md":   writeCases(results, rows),
		"knobs.yaml": knobsYAML(),
	}
	if len(golden) > 0 {
		files["golden.md"] = writeGolden(golden)
	}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			return err
		}
	}
	fmt.Print(summary)
	return nil
}

// ---------- knobs ----------

func applyKnobs(path string, sets []string) ([]string, error) {
	var applied []string
	set := func(k, v string) error {
		if err := tuning.Set(k, v); err != nil {
			return err
		}
		applied = append(applied, k+"="+v)
		return nil
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		defaults := map[string]string{}
		for _, k := range tuning.Knobs() {
			defaults[k.Name] = k.Value
		}
		for _, k := range keys {
			v := fmt.Sprint(m[k])
			if defaults[k] == v {
				continue // same as default: not an override
			}
			if err := set(k, v); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
	}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return nil, fmt.Errorf("-set %q: want NAME=VALUE", s)
		}
		if err := set(strings.TrimSpace(k), strings.TrimSpace(v)); err != nil {
			return nil, err
		}
	}
	return applied, nil
}

func dumpKnobs(w io.Writer) {
	fmt.Fprintln(w, "# Every turnable knob with its compiled-in default (internal/tuning).")
	fmt.Fprintln(w, "# Copy to a new file, keep only what you change, run: testing/run.sh -knobs <file>")
	fmt.Fprintln(w, "# Seed-time knobs only take effect together with -reseed.")
	seedHdr := false
	fmt.Fprintln(w, "\n# --- query time (instant) ---")
	for _, k := range tuning.Knobs() {
		if k.Seed && !seedHdr {
			fmt.Fprintln(w, "\n# --- seed time (needs -reseed) ---")
			seedHdr = true
		}
		fmt.Fprintf(w, "%s: %s\n", k.Name, k.Value)
	}
}

func knobsYAML() string {
	var b strings.Builder
	dumpKnobs(&b)
	return strings.Replace(b.String(), "with its compiled-in default", "as used in this run", 1)
}

// ---------- inputs ----------

func readJSONL(path string) ([]spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []spec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s spec
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		for _, re := range s.MustNot {
			if _, err := regexp.Compile("(?i)" + re); err != nil {
				return nil, fmt.Errorf("%s:%d: must_not_contain %q: %w", path, n, re, err)
			}
		}
		out = append(out, s)
	}
	return out, sc.Err()
}

// mdRow is one row of the e2e.md table: the human expected output.
type mdRow struct{ Category, Question, Expected, MustMention, Source string }

var rowRe = regexp.MustCompile(`^\|\s*\d+\s*\|`)

func readMD(path string) (map[int]mdRow, error) {
	rows := map[int]mdRow{}
	if path == "" {
		return rows, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return rows, nil
	} else if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !rowRe.MatchString(line) {
			continue
		}
		cells := splitRow(line)
		if len(cells) < 6 {
			continue
		}
		var id int
		fmt.Sscan(cells[0], &id)
		rows[id] = mdRow{cells[1], cells[2], cells[3], cells[4], cells[5]}
	}
	return rows, nil
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(strings.TrimSuffix(line, "|"), "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if line[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(line[i])
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

func loadData(dir string) (data.Data, error) {
	if dir == "" {
		return data.Load()
	}
	return data.LoadFS(os.DirFS(dir))
}

func loadBundle(dir string) (index.Bundle, error) {
	if dir == "" {
		return app.LoadBundle()
	}
	return index.Read(os.DirFS(dir))
}

func reseedInto(d data.Data, out string) (string, error) {
	cfg, err := config.LoadSeeder()
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "e2e: seeding into %s (embedder %s/%s) …\n", out, cfg.Embedder, cfg.EmbedModel)
	sum, err := app.NewSeeder(cfg, d, out, ".work").Run(context.Background())
	if err != nil {
		return "", fmt.Errorf("reseed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "e2e: seeded %d chunks, %d pieces, %d warnings\n", sum.Chunks, sum.Pieces, len(sum.Warnings))
	return out, nil
}

// ---------- in-process capture ----------

// recorder is a domain.LLM that records requests and never answers, like a
// dead LLM URL: the service then falls back to search_only.
type recorder struct {
	mu   sync.Mutex
	reqs []domain.LLMRequest
}

func (r *recorder) Complete(_ context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	return domain.LLMResponse{}, errors.New("e2e capture: no LLM call made")
}

type capturer struct {
	rec *recorder
	ans domain.Answerer
}

func newCapturer(cfg config.Server, d data.Data, b index.Bundle) *capturer {
	rec := &recorder{}
	ans, _ := app.NewAnswererWithLLM(cfg, d, b, rec)
	return &capturer{rec: rec, ans: ans}
}

func (c *capturer) capture(q string) capture {
	c.rec.mu.Lock()
	c.rec.reqs = nil
	c.rec.mu.Unlock()
	start := time.Now()
	res := c.ans.Answer(context.Background(), q)
	out := capture{Mode: string(res.Mode), Elapsed: time.Since(start)}
	c.rec.mu.Lock()
	defer c.rec.mu.Unlock()
	out.Calls = len(c.rec.reqs)
	for _, r := range c.rec.reqs {
		if strings.HasPrefix(r.User, "Question:\n") && strings.Contains(r.User, "\n\nContext passages:\n") {
			out.LLM, out.System, out.User, out.Model = true, r.System, r.User, r.Model
		}
	}
	return out
}

// ---------- server capture ----------

func questions(cs []spec) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Question
	}
	return out
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func serverCaptures(dir string, qs []string) (map[string]capture, error) {
	bin := filepath.Join(dir, "olake-server")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/server").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("build server: %v\n%s", err, out)
	}
	defer os.Remove(bin) // ~30 MB; the run keeps server.log
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	dead, err := freePort() // nothing listens here
	if err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "server.log")
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "DEBUG=on", "LLM_API_KEY=dummy", "LLM_MODEL=capture",
		fmt.Sprintf("LLM_BASE_URL=http://127.0.0.1:%d/v1", dead), fmt.Sprintf("PORT=%d", port), "LLM_PROVIDER=openai_compatible", "LLM_FALLBACK_MODEL=")
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for i := 0; ; i++ {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 120 {
			return nil, errors.New("server did not become healthy")
		}
		time.Sleep(250 * time.Millisecond)
	}
	modes := map[string]string{}
	gap := time.Minute/time.Duration(tuning.RatePerIPPerMin) + 500*time.Millisecond
	for i, q := range qs {
		if i >= tuning.RateBurst-1 {
			time.Sleep(gap) // stay under the per-IP rate limit
		}
		body, _ := json.Marshal(map[string]string{"question": q})
		resp, err := http.Post(base+"/ask", "application/json", strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		modes[q] = resp.Header.Get("X-Ask-Mode")
		fmt.Fprintf(os.Stderr, "e2e: server %d/%d %s\n", i+1, len(qs), modes[q])
	}
	time.Sleep(300 * time.Millisecond)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	out := map[string]capture{}
	for q, m := range modes {
		out[q] = capture{Mode: m}
	}
	rule := strings.Repeat("─", 72)
	reqRe := regexp.MustCompile(regexp.QuoteMeta(rule) + `\nLLM REQUEST[^\n]*\n` + regexp.QuoteMeta(rule) + `\n(?s)(.*?)\n` + regexp.QuoteMeta(rule))
	for _, m := range reqRe.FindAllStringSubmatch(string(raw), -1) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct{ Role, Content string }
		}
		if json.Unmarshal([]byte(m[1]), &req) != nil || len(req.Messages) < 2 {
			continue
		}
		user := req.Messages[1].Content
		if !strings.HasPrefix(user, "Question:\n") {
			continue
		}
		q := strings.SplitN(strings.TrimPrefix(user, "Question:\n"), "\n\nContext passages:\n", 2)[0]
		c := out[q]
		c.Calls++
		c.LLM, c.System, c.User, c.Model = true, req.Messages[0].Content, user, req.Model
		out[q] = c
	}
	return out, nil
}

// ---------- evaluation ----------

var passageRe = regexp.MustCompile(`(?m)^\[(\d+)\] \[([^\]]+)\] (.*)\nURL: (\S+)$`)

func parsePassages(user string) []passage {
	_, ctx, ok := strings.Cut(user, "\n\nContext passages:\n")
	if !ok {
		return nil
	}
	idx := passageRe.FindAllStringSubmatchIndex(ctx, -1)
	out := make([]passage, 0, len(idx))
	for i, m := range idx {
		end := len(ctx)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		var n int
		fmt.Sscan(ctx[m[2]:m[3]], &n)
		out = append(out, passage{N: n, Label: ctx[m[4]:m[5]], Title: ctx[m[6]:m[7]], URL: ctx[m[8]:m[9]], Text: strings.TrimSpace(ctx[m[1]:end])})
	}
	return out
}

func fold(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func evaluate(s spec, c capture) result {
	r := result{Spec: s, Cap: c, Passages: parsePassages(c.User)}
	switch {
	case c.LLM:
		r.Outcome = "llm"
	case c.Mode == string(domain.ModeRefused):
		r.Outcome = "refused"
	default:
		r.Outcome = c.Mode
	}
	want := s.Outcome
	if want == "" {
		want = "llm"
	}
	for _, w := range strings.Split(want, "|") {
		if strings.TrimSpace(w) == r.Outcome {
			r.OutcomeOK = true
		}
	}
	// Facts and sources are judged on the context passages (titles + text),
	// never on the question itself.
	var ctx strings.Builder
	for i, p := range r.Passages {
		ctx.WriteString(p.Title + "\n" + p.Text + "\n")
		if r.SrcRank == 0 {
			for _, e := range s.Expect {
				if strings.Contains(p.URL, e) {
					r.SrcRank = i + 1
				}
			}
		}
	}
	text := fold(ctx.String())
	for _, f := range s.MustContain {
		if !strings.Contains(text, fold(f)) {
			r.Missing = append(r.Missing, f)
		}
	}
	for _, re := range s.MustNot {
		if regexp.MustCompile("(?i)" + re).MatchString(ctx.String()) {
			r.Forbidden = append(r.Forbidden, re)
		}
	}
	srcOK := len(s.Expect) == 0 || r.SrcRank > 0
	if r.Outcome != "llm" { // nothing reached the LLM: only the outcome counts
		srcOK = len(s.Expect) == 0
	}
	r.Pass = r.OutcomeOK && srcOK && len(r.Missing) == 0 && len(r.Forbidden) == 0
	return r
}

// ---------- reports ----------

type runMeta struct {
	Dir, Mode, Spec, Golden, Data, Index, Version, Vectors string
	Chunks                                                 int
	Overrides                                              []string
}

func caseID(s spec, i int) int {
	if s.ID > 0 {
		return s.ID
	}
	return i + 1
}

func writeSummary(m runMeta, rs []result, golden []result) string {
	var b strings.Builder
	pass := 0
	for _, r := range rs {
		if r.Pass {
			pass++
		}
	}
	fmt.Fprintf(&b, "# askolake_llm_log vs expected_output — %d/%d pass\n\n", pass, len(rs))
	fmt.Fprintf(&b, "- run: `%s` (%s)\n- spec: `%s` · index: %s `%s` · %d chunks · vectors %s · data: %s\n", m.Dir, m.Mode, m.Spec, m.Index, m.Version, m.Chunks, orDefault(m.Vectors, "none"), m.Data)
	fmt.Fprintf(&b, "- knob overrides: %s\n\n", orDefault(strings.Join(m.Overrides, ", "), "none (defaults)"))
	b.WriteString("| # | Result | Outcome (got/want) | Source rank | Facts | Missing / forbidden |\n|---|---|---|---|---|---|\n")
	for i, r := range rs {
		want := orDefault(r.Spec.Outcome, "llm")
		src := "—"
		if len(r.Spec.Expect) > 0 {
			src = "missing"
			if r.SrcRank > 0 {
				src = fmt.Sprintf("[%d]", r.SrcRank)
			}
		}
		facts := "—"
		if n := len(r.Spec.MustContain); n > 0 {
			facts = fmt.Sprintf("%d/%d", n-len(r.Missing), n)
		}
		problems := strings.Join(r.Missing, ", ")
		if len(r.Forbidden) > 0 {
			problems = strings.TrimPrefix(problems+"; forbidden: "+strings.Join(r.Forbidden, ", "), "; ")
		}
		fmt.Fprintf(&b, "| %d | %s | %s / %s | %s | %s | %s |\n", caseID(r.Spec, i), map[bool]string{true: "PASS", false: "**FAIL**"}[r.Pass], r.Outcome, want, src, facts, problems)
	}
	if len(golden) > 0 {
		g := goldenScore(golden)
		fmt.Fprintf(&b, "\n**Golden (`%s`, same settings):** recall %.3f · MRR %.3f · all facts %d/%d · facts %d/%d · no LLM call (floored/refused) %d\n",
			m.Golden, g.recall, g.mrr, g.fullQ, g.n, g.facts, g.factsTotal, g.noLLM)
	}
	b.WriteString("\nDetails: `cases.md` (expected_output vs askolake_llm_log per case), `requests/NN.txt` (exact LLM request), `knobs.yaml`")
	if len(golden) > 0 {
		b.WriteString(", `golden.md`")
	}
	b.WriteString(".\n")
	return b.String()
}

func writeCases(rs []result, rows map[int]mdRow) string {
	var b strings.Builder
	b.WriteString("# Cases: expected_output (e2e.md) vs askolake_llm_log (captured LLM request)\n")
	for i, r := range rs {
		id := caseID(r.Spec, i)
		fmt.Fprintf(&b, "\n## %d. %s — %s\n\n", id, r.Spec.Question, map[bool]string{true: "PASS", false: "FAIL"}[r.Pass])
		if row, ok := rows[id]; ok {
			fmt.Fprintf(&b, "**expected_output** (e2e.md · %s · source: %s)\n\n> %s\n>\n> Must mention: %s\n\n", row.Category, orDefault(row.Source, "—"), row.Expected, orDefault(row.MustMention, "—"))
		}
		fmt.Fprintf(&b, "**checks:** outcome %s (want %s) · expected URL %v → rank %d · facts missing %v · forbidden %v\n\n",
			r.Outcome, orDefault(r.Spec.Outcome, "llm"), r.Spec.Expect, r.SrcRank, r.Missing, r.Forbidden)
		if !r.Cap.LLM {
			fmt.Fprintf(&b, "**askolake_llm_log:** no LLM request (mode `%s`).\n", r.Cap.Mode)
			continue
		}
		fmt.Fprintf(&b, "**askolake_llm_log:** %d passages, %d chars (`requests/%02d.txt`)\n\n", len(r.Passages), len(r.Cap.User), id)
		for _, p := range r.Passages {
			var has []string
			for _, f := range r.Spec.MustContain {
				if strings.Contains(fold(p.Title+"\n"+p.Text), fold(f)) {
					has = append(has, f)
				}
			}
			mark := ""
			for _, e := range r.Spec.Expect {
				if strings.Contains(p.URL, e) {
					mark = " ← expected source"
				}
			}
			fmt.Fprintf(&b, "- [%d] %s — %s%s", p.N, p.Label, p.URL, mark)
			if len(has) > 0 {
				fmt.Fprintf(&b, " · facts: %s", strings.Join(has, ", "))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

type gscore struct {
	recall, mrr                        float64
	n, fullQ, facts, factsTotal, noLLM int
}

func goldenScore(rs []result) gscore {
	var g gscore
	g.n = len(rs)
	for _, r := range rs {
		if !r.Cap.LLM {
			g.noLLM++
		}
		if r.SrcRank > 0 {
			g.recall++
			g.mrr += 1 / float64(r.SrcRank)
		}
		g.factsTotal += len(r.Spec.MustContain)
		g.facts += len(r.Spec.MustContain) - len(r.Missing)
		if len(r.Missing) == 0 {
			g.fullQ++
		}
	}
	if g.n > 0 {
		g.recall /= float64(g.n)
		g.mrr /= float64(g.n)
	}
	return g
}

func writeGolden(rs []result) string {
	var b strings.Builder
	b.WriteString("# Golden questions with the same settings\n\n| Question | Source rank | Missing facts |\n|---|---|---|\n")
	for _, r := range rs {
		if r.SrcRank > 0 && len(r.Missing) == 0 {
			continue
		}
		src := "missing"
		if r.SrcRank > 0 {
			src = fmt.Sprint(r.SrcRank)
		}
		if !r.Cap.LLM {
			src = "no LLM call (" + r.Cap.Mode + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", r.Spec.Question, src, strings.Join(r.Missing, ", "))
	}
	return b.String()
}

func requestText(c capture) string {
	if !c.LLM {
		return fmt.Sprintf("No LLM request was made (mode %q).\n", c.Mode)
	}
	req := map[string]any{"model": c.Model, "messages": []map[string]string{{"role": "system", "content": c.System}, {"role": "user", "content": c.User}}}
	raw, _ := json.MarshalIndent(req, "", "  ")
	return fmt.Sprintf("LLM calls: %d · mode after fallback: %s\n\n%s\n\n--- user content (readable) ---\n%s\n", c.Calls, c.Mode, raw, c.User)
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safeName(s string) string { return strings.Trim(unsafe.ReplaceAllString(s, "-"), "-") }
