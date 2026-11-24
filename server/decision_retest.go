package server

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Retest verdict split: the retester agent gathers evidence and rules; the
// decision lane independently re-reads the recorded evidence and verdict. A
// disagreement is surfaced for the operator/judge-stats loop, never applied.

// adjudicateRetest re-checks a saved retest verdict against its evidence.
func (s *Server) adjudicateRetest(ctx context.Context, conversationID int64) {
	if s.decisionLaneDisabled() {
		return
	}
	r, err := s.m.pg.FindingRetestForConversation(ctx, conversationID)
	if err != nil || r == nil || r.Verdict == "" {
		return
	}
	f, err := s.m.pg.GetFinding(r.FindingID)
	if err != nil || f == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "A retest of a recorded vulnerability finished. Judge whether the verdict follows from the evidence of THIS run.\n\n")
	fmt.Fprintf(&b, "Original finding #%d — %s (%s, severity %s)\n", f.ID, f.Name, f.VulnClass, f.Severity)
	fmt.Fprintf(&b, "Original summary: %s\n\n", clipStr(f.Summary, 1500))
	fmt.Fprintf(&b, "Retest verdict: %s\n", r.Verdict)
	fmt.Fprintf(&b, "Retest summary: %s\n", clipStr(r.Summary, 2500))
	fmt.Fprintf(&b, "Retest evidence: %s\n", clipStr(r.Evidence, 6000))
	options := []string{"reproduced", "fixed", "inconclusive"}
	decision, conf, reason, pid, err := s.oneShotDecision(ctx, b.String(), options, 11000)
	if err != nil {
		log.Printf("[decision:retest] conv %d skipped: %v", conversationID, err)
		return
	}
	taskID := ""
	if f.TaskID != nil {
		taskID = fmt.Sprint(*f.TaskID)
	}
	agree := strings.EqualFold(decision, r.Verdict)
	s.recordDecision(DecisionScopeRetest, conversationID, taskID, b.String(), r.Verdict, decision, reason, conf, pid, agree)
	if !agree {
		s.emitDecisionActivity(taskID, fmt.Sprintf("decision model disagrees with retest verdict on finding %d: agent said %s, adjudicated %s (%s)", f.ID, r.Verdict, decision, clipStr(reason, 160)))
	}
}
