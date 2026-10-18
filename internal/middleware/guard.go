package middleware

import (
	"context"
	"regexp"

	"olake-ask/internal/ask"
	"olake-ask/internal/domain"
	"olake-ask/internal/tuning"
	"olake-ask/internal/utils"
)

var injectionRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(ignore|disregard|forget)\b.{0,30}\b(previous|prior|above|all|earlier)\b.{0,30}\b(instructions?|prompts?|rules?)\b`),
	regexp.MustCompile(`(?i)\b(reveal|show|print|repeat|output)\b.{0,30}\b(system|hidden|initial)\b.{0,10}\b(prompt|instructions?|message)\b`),
	regexp.MustCompile(`(?i)\byou are now\b|\bdeveloper mode\b|\bjailbreak\b|\bDAN\b`),
}

type guards struct{ next domain.Answerer }

// WithGuards redacts secrets and refuses too-short or injection questions.
func WithGuards(next domain.Answerer) domain.Answerer { return guards{next: next} }

func (g guards) Answer(ctx context.Context, q string) domain.Result {
	q = utils.Redact(q)
	if len([]rune(q)) < tuning.MinQuestionChars {
		return ask.Refused("too_short")
	}
	for _, re := range injectionRes {
		if re.MatchString(q) {
			return ask.Refused("injection")
		}
	}
	return g.next.Answer(ctx, q)
}
