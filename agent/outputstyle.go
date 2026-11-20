package agent

import (
	"encoding/base64"
	"strings"
)

// outputStyleBlock is the code-owned token-discipline tail appended to every
// agent system prompt (worker, planner, mainagent, every chat agent —
// auto/pentest/reporter/retester/custom — and the cold-digest compactor).
// The models default to polite full-sentence prose with filler everywhere:
// reasoning, messages, summaries, tool inputs. This block switches them to
// compressed technical English: same meaning, a fraction of the tokens. It is
// appended AFTER the editable body like the injection tails, so a DB prompt
// override can never drop it, and it never needs a prompt reseed.
const outputStyleBlock = `

Output style (token discipline, applies to ALL your text — reasoning, messages, summaries, intent descriptions, facts, tool arguments):
- Compressed technical English. Drop filler ("it appears that", "please note", "we can see that", "in order to"), drop ceremony, no intros, no_outros, no restating the question.
- Fragments allowed. Short sentences. Simple exact words, no synonym rotation — same term for same thing every time.
- Never drop: negatives, qualifiers that flip meaning, required evidence, measured numbers with denominators, identifiers, code, commands, error strings (verbatim).
- Findings and facts keep every mandatory field; compression cuts words, not substance.
- Deliverable Markdown reports keep the template's full structure.`

// outputStyleTail returns the style block to append after body, or "" when the
// body already carries it verbatim (never render twice).
func outputStyleTail(body string) string {
	if strings.Contains(body, "Output style (token discipline") {
		return ""
	}
	return outputStyleBlock
}

// psEncode64Plain encodes raw bytes (script content) as std base64.
func psEncode64Plain(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
