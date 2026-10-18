package ask

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/search"
)

func loadData(t *testing.T) data.Data {
	t.Helper()
	d, err := data.Load()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func fixtureChunks() []domain.Chunk {
	mk := func(id int, url, title, text string) domain.Chunk {
		page := strings.SplitN(url, "#", 2)[0]
		return domain.Chunk{ID: id, URL: url, Page: page, Title: title, Text: text, Source: domain.SourceDocs, Product: domain.ProductOLake}
	}
	return []domain.Chunk{
		mk(0, "https://olake.io/docs/connectors/postgres/#cdc", "Postgres › CDC", "Enable logical replication and create a replication slot named olake_slot. Set `replication_slot` in source.json."),
		mk(1, "https://olake.io/docs/connectors/postgres/#config", "Postgres › Config", "The postgres source.json needs host, port, database and replication_slot for CDC sync."),
		mk(2, "https://olake.io/docs/connectors/mysql/", "MySQL", "Enable binlog for MySQL CDC."),
		mk(3, "https://olake.io/docs/install/kubernetes/", "Helm", "Deploy OLake on Kubernetes with the Helm chart."),
	}
}

func newRetriever(t *testing.T) *search.Retriever {
	d := loadData(t)
	ch := fixtureChunks()
	return search.NewRetriever(search.NewPrep(d), ch, search.NewKeyword(ch), nil, search.NewErrors(ch), d.Deprecated)
}

type fakeLLM struct {
	mu      sync.Mutex
	calls   []domain.LLMRequest
	respond func(req domain.LLMRequest) (string, error)
}

func (f *fakeLLM) Complete(_ context.Context, req domain.LLMRequest) (domain.LLMResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	text, err := f.respond(req)
	if err != nil {
		return domain.LLMResponse{}, err
	}
	return domain.LLMResponse{Text: text, InTokens: 10, OutTokens: 5}, nil
}

const goodAnswer = "ANSWERABLE: yes\nANSWER:\nCreate a replication slot [1] and set `replication_slot` [2]."

func answerOnly(req domain.LLMRequest) (string, error) {
	if req.System == AnswerSystem {
		return goodAnswer, nil
	}
	return "", errors.New("unexpected call")
}

const cdcQuestion = "How do I enable postgres CDC with a replication slot?"

func TestParseAnswerVariants(t *testing.T) {
	ok, body := ParseAnswer("**ANSWERABLE:** **yes**\n**ANSWER:**\nUse X [1].", 2)
	if !ok || body != "Use X [1]." {
		t.Fatalf("bold: %v %q", ok, body)
	}
	ok, body = ParseAnswer("ANSWERABLE: no\nThe passages do not cover this.", 2)
	if ok || body != "The passages do not cover this." {
		t.Fatalf("missing ANSWER: %v %q", ok, body)
	}
	if ok, _ := ParseAnswer("Use the slot 【1】.", 2); !ok {
		t.Fatal("missing ANSWERABLE with citation should be yes")
	}
	if ok, _ := ParseAnswer("Use the slot [7].", 2); ok {
		t.Fatal("missing ANSWERABLE with out-of-range citation should be no")
	}
}

func TestParseRewriteAndRerank(t *testing.T) {
	got := ParseRewrite("1. postgres cdc setup\n- \"replication slot\"\n\n• pgoutput\nfourth")
	if len(got) != 3 || got[0] != "postgres cdc setup" || got[1] != "replication slot" || got[2] != "pgoutput" {
		t.Fatalf("rewrite = %q", got)
	}
	ids, conf, ok := ParseRerank("IDS: 3, 1,2\nCONFIDENCE: High")
	if !ok || conf != "high" || len(ids) != 3 || ids[0] != 3 {
		t.Fatalf("rerank = %v %q %v", ids, conf, ok)
	}
	if _, _, ok := ParseRerank("IDS: 1"); ok {
		t.Fatal("missing confidence accepted")
	}
}

func TestVerifyCitations(t *testing.T) {
	d := loadData(t)
	sel := fixtureChunks()
	sel[1].URL = sel[0].URL // duplicate URL → same index
	v := Verify(true, "A [3] and [1][2] then [9] and [source: 4] and [1, 3].", sel, "q", nil, d)
	if !v.OK {
		t.Fatal("not OK")
	}
	want := "A [1] and [2] then  and [3] and [2][1]."
	if v.Answer != want {
		t.Fatalf("answer = %q, want %q", v.Answer, want)
	}
	if len(v.Sources) != 3 || v.Sources[0] != sel[2].URL || v.Sources[1] != sel[0].URL || v.Sources[2] != sel[3].URL {
		t.Fatalf("sources = %v", v.Sources)
	}
	if v := Verify(true, "No citations here, and code `arr[1]`.", sel, "q", nil, d); v.OK {
		t.Fatal("zero citations accepted")
	}
	if v := Verify(false, "Fine [1].", sel, "q", nil, d); v.OK {
		t.Fatal("not answerable accepted")
	}
}

func TestVerifyURLsIdentifiersDeprecated(t *testing.T) {
	d := loadData(t)
	sel := fixtureChunks()
	body := "See [docs](https://olake.io/docs/connectors/postgres/) [1], [evil](https://evil.example/x) and https://phish.example/a. " +
		"Also https://github.com/datazip-inc/olake/issues/1 [2]. Set `replication_slot`, `max_wal_senders.magic` and use wal2json [1]."
	v := Verify(true, body, sel, "how to set up cdc", map[string]struct{}{"max_wal_senders": {}}, d)
	if !v.OK {
		t.Fatal("not OK")
	}
	if strings.Contains(v.Answer, "evil.example") || strings.Contains(v.Answer, "phish.example") || !strings.Contains(v.Answer, ", evil and ") {
		t.Fatalf("untrusted URL kept: %q", v.Answer)
	}
	if !strings.Contains(v.Answer, "[docs](https://olake.io/docs/connectors/postgres/)") || !strings.Contains(v.Answer, "https://github.com/datazip-inc/olake/issues/1") {
		t.Fatalf("trusted URL removed: %q", v.Answer)
	}
	if !strings.Contains(v.Answer, "verify before using: `max_wal_senders.magic`.\n") {
		t.Fatalf("identifier note wrong: %q", v.Answer)
	}
	if !strings.Contains(v.Answer, "Note: OLake has deprecated wal2json-based CDC") {
		t.Fatalf("deprecated note missing: %q", v.Answer)
	}
	if v := Verify(true, "Use wal2json [1].", sel, "is wal2json supported?", nil, d); strings.Contains(v.Answer, "Note:") {
		t.Fatalf("note added although question mentions term: %q", v.Answer)
	}
}

func TestServiceFloorDontKnowNoLLM(t *testing.T) {
	llm := &fakeLLM{respond: answerOnly}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Data: loadData(t)})
	r := s.Answer(context.Background(), "What is the weather in Paris today?")
	if r.Mode != domain.ModeDontKnow || r.LLMCalls != 0 || len(llm.calls) != 0 || r.Sources == nil || len(r.Sources) != 0 {
		t.Fatalf("result = %+v calls=%d", r, len(llm.calls))
	}
	if strings.Contains(r.Answer, "may be related") || !strings.Contains(r.Answer, "https://olake.io/slack/") {
		t.Fatalf("floor answer = %q", r.Answer)
	}
}

func TestServiceSearchOnlyWithoutLLM(t *testing.T) {
	s := NewService(Options{Retriever: newRetriever(t), Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeSearchOnly || len(r.Sources) == 0 || !strings.Contains(r.Answer, "[1]") {
		t.Fatalf("result = %+v", r)
	}
	// Markers index into sources.
	for i := range r.Sources {
		if !strings.Contains(r.Answer, "["+string(rune('1'+i))+"]") {
			t.Fatalf("missing marker %d in %q", i+1, r.Answer)
		}
	}
}

func TestServiceDefaultOneCall(t *testing.T) {
	llm := &fakeLLM{respond: answerOnly}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeAnswered || r.LLMCalls != 1 || len(llm.calls) != 1 || r.InTok != 10 || r.OutTok != 5 {
		t.Fatalf("result = %+v calls=%d", r, len(llm.calls))
	}
	if len(r.Sources) == 0 || !strings.HasPrefix(r.Sources[0], "https://olake.io/docs/connectors/postgres/") {
		t.Fatalf("sources = %v", r.Sources)
	}
}

func TestServiceFallbackModel(t *testing.T) {
	llm := &fakeLLM{respond: func(req domain.LLMRequest) (string, error) {
		if req.Model == "primary" {
			return "", errors.New("503")
		}
		return goodAnswer, nil
	}}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "primary", Fallback: "backup", Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeAnswered || len(llm.calls) != 2 || llm.calls[1].Model != "backup" {
		t.Fatalf("result = %+v calls=%+v", r, llm.calls)
	}
	// Without a fallback the failure degrades to search-only.
	s = NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "primary", Data: loadData(t)})
	if r := s.Answer(context.Background(), cdcQuestion); r.Mode != domain.ModeSearchOnly {
		t.Fatalf("result = %+v", r)
	}
}

func TestServiceBudgetErrorSkipsFallback(t *testing.T) {
	llm := &fakeLLM{respond: func(domain.LLMRequest) (string, error) { return "", domain.ErrBudget }}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Fallback: "f", Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeSearchOnly || len(llm.calls) != 1 || r.LLMCalls != 0 {
		t.Fatalf("result = %+v calls=%d", r, len(llm.calls))
	}
}

type exhausted bool

func (e exhausted) Exhausted() bool { return bool(e) }

func TestServiceExhaustedBudgetSearchOnly(t *testing.T) {
	llm := &fakeLLM{respond: answerOnly}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Budget: exhausted(true), Model: "m", Data: loadData(t)})
	if r := s.Answer(context.Background(), cdcQuestion); r.Mode != domain.ModeSearchOnly || len(llm.calls) != 0 {
		t.Fatalf("result = %+v", r)
	}
}

func TestServiceRerankNone(t *testing.T) {
	llm := &fakeLLM{respond: func(req domain.LLMRequest) (string, error) {
		if req.System == RerankPrompt {
			return "IDS: 1\nCONFIDENCE: none", nil
		}
		return goodAnswer, nil
	}}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Rerank: true, Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeDontKnow || len(llm.calls) != 1 || !strings.Contains(r.Answer, "These pages may be related") {
		t.Fatalf("result = %+v", r)
	}
}

func TestServiceRerankSelectsAndFailsSafe(t *testing.T) {
	var answerUser string
	llm := &fakeLLM{respond: func(req domain.LLMRequest) (string, error) {
		if req.System == RerankPrompt {
			return "IDS: 2, 2, 99\nCONFIDENCE: high", nil
		}
		answerUser = req.User
		return "ANSWERABLE: yes\nANSWER:\nUse it [1].", nil
	}}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Rerank: true, Data: loadData(t)})
	r := s.Answer(context.Background(), cdcQuestion)
	if r.Mode != domain.ModeAnswered || r.LLMCalls != 2 || strings.Count(answerUser, "URL: ") != 1 {
		t.Fatalf("result = %+v context=%q", r, answerUser)
	}
	llm.respond = func(req domain.LLMRequest) (string, error) {
		if req.System == RerankPrompt {
			return "garbage", nil
		}
		return goodAnswer, nil
	}
	if r := s.Answer(context.Background(), cdcQuestion); r.Mode != domain.ModeAnswered {
		t.Fatalf("unparsable rerank should fall back: %+v", r)
	}
}

func TestServiceRewriteFailureIgnored(t *testing.T) {
	for _, mode := range []string{"always", "when_needed"} {
		llm := &fakeLLM{respond: func(req domain.LLMRequest) (string, error) {
			if req.System == RewritePrompt {
				return "", context.DeadlineExceeded
			}
			return goodAnswer, nil
		}}
		s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Rewrite: mode, Data: loadData(t)})
		r := s.Answer(context.Background(), "postgres cdc slot olake")
		if r.Mode != domain.ModeAnswered {
			t.Fatalf("%s: result = %+v", mode, r)
		}
	}
}

func TestServiceNotAnswerable(t *testing.T) {
	llm := &fakeLLM{respond: func(domain.LLMRequest) (string, error) {
		return "ANSWERABLE: no\nANSWER:\nThe passages do not say.", nil
	}}
	s := NewService(Options{Retriever: newRetriever(t), LLM: llm, Model: "m", Data: loadData(t)})
	if r := s.Answer(context.Background(), cdcQuestion); r.Mode != domain.ModeDontKnow || !strings.Contains(r.Answer, "may be related") {
		t.Fatalf("result = %+v", r)
	}
}

func TestBuildContextAndRefused(t *testing.T) {
	ch := fixtureChunks()
	ch[1].Deprecated = true
	ch[2].Source = domain.SourceIssue
	ctx := BuildContext(ch[:3])
	for _, want := range []string{"[1] [Docs] Postgres › CDC\nURL: https://olake.io/docs/connectors/postgres/#cdc\n", "[2] [Docs (deprecated)]", "[3] [GitHub issue] MySQL"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("context missing %q:\n%s", want, ctx)
		}
	}
	if r := Refused("too_short"); r.Mode != domain.ModeRefused || r.Answer != tooShortText || r.Sources == nil {
		t.Fatalf("refused = %+v", r)
	}
	if r := Refused("injection"); r.Answer != refusedText {
		t.Fatalf("refused = %+v", r)
	}
}

func TestBelowFloorPerModel(t *testing.T) {
	weak := search.Retrieval{Hits: []domain.Hit{{ChunkID: 0}}, MaxCoverage: 0.1}
	cases := []struct {
		cos, floor float64
		want       bool
	}{
		{0.49, 0, true},     // no per-model floor: tuning.FloorCosine (0.50)
		{0.50, 0, false},    // at the default floor
		{0.40, 0.35, false}, // a model with a lower calibrated floor
		{0.40, 0.45, true},
	}
	for _, c := range cases {
		r := weak
		r.MaxCosine, r.CosineFloor = c.cos, c.floor
		if got := BelowFloor(r, true); got != c.want {
			t.Errorf("cos=%v floor=%v: below=%v, want %v", c.cos, c.floor, got, c.want)
		}
	}
	if !BelowFloor(search.Retrieval{Hits: []domain.Hit{{}}, MaxCosine: 0.9, CosineFloor: 0.5}, false) {
		t.Error("vectors off must ignore cosine")
	}
	if BelowFloor(search.Retrieval{Hits: []domain.Hit{{}}, ErrHits: 1}, true) {
		t.Error("error match must pass the floor")
	}
}
