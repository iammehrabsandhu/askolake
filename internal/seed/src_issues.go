package seed

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"olake-ask/internal/client"
	"olake-ask/internal/data"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

var (
	issueSignalRe = regexp.MustCompile(`(?i)error|exception|failed|panic|caused by`)
	blankRunsRe   = regexp.MustCompile(`\n{3,}`)
)

var maintainerRoles = map[string]bool{"OWNER": true, "MEMBER": true, "COLLABORATOR": true}

// Issues collects closed issues that have at least one maintainer reply.
func Issues(ctx context.Context, gh *client.GitHub, cfg data.Issues, authenticated bool) ([]domain.Chunk, error) {
	var out []domain.Chunk
	commentFetches := 0
	warnedCap := false
	var firstErr error
	for _, repo := range cfg.Repos {
		issues, err := gh.ListClosedIssues(ctx, repo, cfg.MaxPerRepo)
		if err != nil {
			slog.Warn("issues: list failed", "repo", repo, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			if len(issues) == 0 {
				continue
			}
		}
		product := domain.ProductOLake
		if strings.HasSuffix(repo, "/olake-fusion") {
			product = domain.ProductFusion
		}
		for _, is := range issues {
			if is.Comments == 0 {
				continue
			}
			if !authenticated && commentFetches >= tuning.UnauthCommentCap {
				if !warnedCap {
					slog.Warn("issues: unauthenticated comment cap reached; set GITHUB_TOKEN for full coverage", "cap", tuning.UnauthCommentCap)
					warnedCap = true
				}
				break
			}
			if r := gh.Remaining(); r >= 0 && r < tuning.MinRateRemaining {
				slog.Warn("issues: GitHub rate limit nearly exhausted, stopping", "remaining", r)
				return out, nil
			}
			comments, err := gh.ListComments(ctx, repo, is.Number)
			commentFetches++
			if err != nil {
				slog.Warn("issues: comments failed", "repo", repo, "issue", is.Number, "err", err)
				continue
			}
			var replies []string
			for _, c := range comments {
				if maintainerRoles[c.AuthorAssociation] && strings.TrimSpace(c.Body) != "" {
					replies = append(replies, strings.TrimSpace(c.Body))
				}
			}
			if len(replies) == 0 {
				continue
			}
			out = append(out, IssueChunks(repo, is, replies, product)...)
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("issues: %w", firstErr)
	}
	return out, nil
}

// IssueChunks builds the chunk(s) for one issue with maintainer replies.
func IssueChunks(repo string, is client.Issue, replies []string, product domain.Product) []domain.Chunk {
	body := trimIssueText(utils.Redact(is.Body), tuning.IssueBodyChars)
	reply := trimIssueText(utils.Redact(strings.Join(replies, "\n\n")), tuning.IssueReplyChars)
	text := "Question:\n" + body + "\n\nMaintainer reply:\n" + reply
	title := fmt.Sprintf("GitHub issue #%d (%s): %s", is.Number, repo, strings.TrimSpace(is.Title))
	c := domain.Chunk{URL: is.HTMLURL, Page: is.HTMLURL, Title: title, Text: text, Source: domain.SourceIssue, Product: product}
	if wc(text) <= tuning.ChunkMaxWords {
		return []domain.Chunk{c}
	}
	var out []domain.Chunk
	for _, part := range chunkBlocks([]Block{{Kind: "p", Text: text}}) {
		cc := c
		cc.Text = part
		out = append(out, cc)
	}
	return out
}

// trimIssueText keeps prose, the first 15 lines of each code fence plus any
// later fence lines that look like errors, collapses blank lines and caps length.
func trimIssueText(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	inFence, fenceLines := false, 0
	for _, l := range strings.Split(s, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			fenceLines = 0
			out = append(out, l)
			continue
		}
		if inFence {
			fenceLines++
			if fenceLines > tuning.IssueFenceLines && !issueSignalRe.MatchString(l) {
				continue
			}
		}
		out = append(out, strings.TrimRight(l, " \t"))
	}
	res := blankRunsRe.ReplaceAllString(strings.Join(out, "\n"), "\n\n")
	res, cut := utils.TruncateChars(strings.TrimSpace(res), max)
	if cut {
		res += " …"
	}
	return res
}
