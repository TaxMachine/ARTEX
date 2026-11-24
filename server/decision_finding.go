package server

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/TaxMachine/ARTEX/agent"
	"github.com/TaxMachine/ARTEX/db"
)

// Finding adjudicators: the decision lane applied to vulnerability records.
// Fired async after a successful Record() so the write path never blocks or
// fails on adjudication; results land in decision_lane + task activity.

// decisionRecorder wraps the evidence store with the adjudicating decorator.
// The inner recorder owns the atomic write; the decorator only fires async
// review after a successful Record.
func (s *Server) decisionRecorder() agent.FindingRecorder {
	return &decisionFindingRecorder{inner: s.evidenceStore(), s: s}
}

// decisionFindingRecorder decorates the evidence store's recorder with
// post-record adjudication.
type decisionFindingRecorder struct {
	inner agent.FindingRecorder
	s     *Server
}

func (d *decisionFindingRecorder) Record(ctx context.Context, in db.RecordFindingInput, refs []db.TrafficRef) (*db.RecordedFinding, error) {
	out, err := d.inner.Record(ctx, in, refs)
	if err != nil {
		return out, err
	}
	if out != nil && out.FindingID > 0 {
		d.s.goAdjudicate(DecisionScopeFinding, out.FindingID, func(ctx context.Context) {
			d.s.adjudicateFinding(ctx, out.FindingID, in)
		})
		d.s.goAdjudicate(DecisionScopeDedupe, out.FindingID, func(ctx context.Context) {
			d.s.adjudicateDedupe(ctx, out.FindingID)
		})
	}
	return out, err
}

// adjudicateFinding re-checks the recorded severity and exploit class against
// the recorded evidence and anatomy. The worker's values stand; a disagreement
// is recorded and surfaced.
func (s *Server) adjudicateFinding(ctx context.Context, findingID int64, in db.RecordFindingInput) {
	if s.decisionLaneDisabled() {
		return
	}
	// Build the question from the record itself (authoritative post-write).
	f, err := s.m.pg.GetFinding(findingID)
	if err != nil || f == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "A vulnerability was recorded. Judge the claimed severity.\n")
	fmt.Fprintf(&b, "Claimed severity: %s\n", f.Severity)
	fmt.Fprintf(&b, "Name: %s\n", f.Name)
	fmt.Fprintf(&b, "Claimed exploit class: %s\n", f.VulnClass)
	if f.TaskDescription != "" {
		fmt.Fprintf(&b, "Task: %s\n", clipStr(f.TaskDescription, 300))
	}
	fmt.Fprintf(&b, "Summary: %s\n", clipStr(f.Summary, 2500))
	if len(f.Evidence) > 0 {
		fmt.Fprintf(&b, "Evidence: %s\n", clipStr(f.Evidence, 6000))
	}
	if len(f.Primitives) > 0 {
		names := make([]string, 0, len(f.Primitives))
		for _, p := range f.Primitives {
			names = append(names, p.Name)
		}
		fmt.Fprintf(&b, "Primitives (anatomy): %s\n", strings.Join(names, "; "))
	}
	if len(f.Chain) > 0 {
		fmt.Fprintf(&b, "Chain steps: %d (", len(f.Chain))
		for i, st := range f.Chain {
			if i > 0 {
				b.WriteString(" -> ")
			}
			fmt.Fprintf(&b, "%s=%s", st.Action, st.Result)
		}
		b.WriteString(")\n")
	}
	if f.PoELevel > 0 {
		fmt.Fprintf(&b, "Declared proof-of-exploitation level: L%d\n", f.PoELevel)
	}
	options := []string{"critical", "high", "medium", "low"}
	v, err := s.oneShotDecision(ctx, DecisionAsk{
		Input: b.String(), Options: options, MaxChars: 12000,
		// Predicates turn the over-claim signal into numbers the operator can
		// read at a glance ("impact demonstrated 0.11, data returned 0.02").
		Predicates: []Predicate{
			{Name: "impact_demonstrated", Statement: "The evidence demonstrates the claimed security impact, not merely a crash or anomaly."},
			{Name: "data_returned", Statement: "Attacker-controlled data is read back or exfiltrated."},
			{Name: "control_demonstrated", Statement: "Control over a pointer, instruction pointer, or offset is demonstrated."},
		},
	})
	if err != nil {
		log.Printf("[decision:finding] f%d skipped: %v", findingID, err)
		return
	}
	taskID := ""
	if f.TaskID != nil {
		taskID = fmt.Sprint(*f.TaskID)
	}
	agree := strings.EqualFold(v.Decision, f.Severity)
	s.recordDecision(DecisionScopeFinding, findingID, taskID, b.String(), f.Severity, v.Decision, v.Reason, v.Confidence, v.ProfileID, agree)
	if !agree {
		s.emitDecisionActivity(taskID, fmt.Sprintf("decision model flags finding %d: severity claimed %s, adjudicated %s (%s)", findingID, f.Severity, v.Decision, clipStr(v.Reason, 160)))
	}
}

// adjudicateDedupe asks whether a just-recorded finding duplicates an existing
// finding of the same project. Candidates are pre-filtered lexically (token
// overlap on name + summary, no DB extension needed) to the top few, so the
// decision model compares a small set rather than the whole project history.
func (s *Server) adjudicateDedupe(ctx context.Context, findingID int64) {
	if s.decisionLaneDisabled() {
		return
	}
	f, err := s.m.pg.GetFinding(findingID)
	if err != nil || f == nil {
		return
	}
	cands := s.dedupeCandidates(findingID, f)
	if len(cands) == 0 {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "A new vulnerability was recorded. Decide whether it duplicates one of the existing project findings listed below (same underlying weakness at the same location), or is distinct.\n\n")
	fmt.Fprintf(&b, "NEW finding #%d\n  class: %s\n  name: %s\n  summary: %s\n\n", findingID, f.VulnClass, f.Name, clipStr(f.Summary, 900))
	options := []string{"distinct"}
	for _, c := range cands {
		fmt.Fprintf(&b, "EXISTING — finding #%d\n  class: %s\n  name: %s\n  summary: %s\n\n", c.ID, c.VulnClass, c.Name, clipStr(c.Summary, 700))
		options = append(options, fmt.Sprintf("duplicate-%d", c.ID))
	}
	b.WriteString("Answer \"distinct\" when none describes the same underlying weakness, or \"duplicate-<finding id>\" naming the finding it repeats.\n")
	v, err := s.oneShotDecision(ctx, DecisionAsk{
		Input: b.String(), Options: options, MaxChars: 12000,
		Predicates: []Predicate{
			{Name: "same_root_cause", Statement: "The new finding and an existing finding share the same underlying root cause."},
			{Name: "same_location", Statement: "The weakness sits at the same code location or attack surface as an existing finding."},
		},
	})
	if err != nil {
		log.Printf("[decision:dedupe] f%d skipped: %v", findingID, err)
		return
	}
	taskID := ""
	if f.TaskID != nil {
		taskID = fmt.Sprint(*f.TaskID)
	}
	isDup := v.Decision != "distinct"
	label := v.Decision
	var dupOf int64
	if isDup {
		var n int64
		if _, err := fmt.Sscanf(v.Decision, "duplicate-%d", &n); err == nil {
			for _, c := range cands {
				if c.ID == n {
					dupOf = n
					break
				}
			}
			if dupOf > 0 {
				label = fmt.Sprintf("duplicate of #%d", dupOf)
			} else {
				// named an id that was not offered: treat as distinct, the
				// option set is the contract
				isDup = false
				label = "distinct (model named an unlisted id)"
			}
		}
	}
	// "agree" here means "the agent's implicit uniqueness claim holds".
	s.recordDecision(DecisionScopeDedupe, findingID, taskID, b.String(), "unique", label, v.Reason, v.Confidence, v.ProfileID, !isDup)
	if isDup {
		s.emitDecisionActivity(taskID, fmt.Sprintf("decision model suspects finding %d duplicates finding #%d (p=%.2f; %s) — review before merging", findingID, dupOf, v.Confidence, clipStr(v.Reason, 160)))
	}
}

// dedupeCandidates returns the most lexically similar prior findings of the
// same project (top dedupeMaxCandidates), never the finding itself.
const dedupeMaxCandidates = 3

func (s *Server) dedupeCandidates(findingID int64, f *db.DBFinding) []*db.DBFinding {
	if s.m.pg == nil {
		return nil
	}
	rows, err := s.m.pg.Query(`SELECT f.id, COALESCE(f.name,''), f.vulnclass, f.summary
		FROM findings f
		WHERE f.project_id IS NOT NULL
		  AND f.project_id = (SELECT project_id FROM findings WHERE id=$1)
		  AND f.id <> $1
		  AND f.status NOT IN ('false_positive','duplicate')
		ORDER BY f.id DESC LIMIT 150`, findingID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	type scored struct {
		f *db.DBFinding
		s float64
	}
	newTokens := tokens(f.Name + " " + f.Summary)
	if len(newTokens) == 0 {
		return nil
	}
	var list []scored
	for rows.Next() {
		c := &db.DBFinding{}
		if rows.Scan(&c.ID, &c.Name, &c.VulnClass, &c.Summary) != nil {
			continue
		}
		overlap := jaccard(newTokens, tokens(c.Name+" "+c.Summary))
		if c.VulnClass == f.VulnClass {
			overlap += 0.15 // same class is a strong prior
		}
		if overlap >= 0.12 {
			list = append(list, scored{c, overlap})
		}
	}
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].s > list[j-1].s; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	out := []*db.DBFinding{}
	for _, x := range list {
		if len(out) >= dedupeMaxCandidates {
			break
		}
		out = append(out, x.f)
	}
	return out
}

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,;:()[]{}\"'`|/_-<>=")
		if len(w) >= 4 {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter, union := 0, 0
	for w := range a {
		if b[w] {
			inter++
		}
		union++
	}
	for w := range b {
		if !a[w] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
