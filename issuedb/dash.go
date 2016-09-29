// Copyright 2016 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
)

type action struct {
	time   string
	op     int
	number int64
	text   string
}

const (
	_ = iota
	opCreate
	opMilestone
	opDemilestone
	opClose
	opReopen
	opLabel
	opUnlabel
)

type issueState struct {
	createTime         string
	closeTime          string
	milestone          string
	needsInvestigation bool
	needsFix           bool
	needsDecision      bool
	blocked            bool
	waitingForInfo     bool
}

func dashActions() ([]action, int) {
	var actions []action
	var maxIssue int64
	var last int64
	for {
		var all []History
		if err := storage.Select(db, &all, "where RowID > ? order by RowID asc limit 100", last); err != nil {
			log.Fatal("sql: %v", err)
		}
		if len(all) == 0 {
			break
		}
		for _, h := range all {
			if maxIssue < h.Issue {
				maxIssue = h.Issue
			}
			switch h.Action {
			case "issue":
				actions = append(actions, action{h.Time, opCreate, h.Issue, ""})
			case "milestone?", "milestoned":
				actions = append(actions, action{h.Time, opMilestone, h.Issue, h.Text})
			case "demilestoned":
				actions = append(actions, action{h.Time, opDemilestone, h.Issue, h.Text})
			case "close?", "closed":
				actions = append(actions, action{h.Time, opClose, h.Issue, ""})
			case "reopened":
				actions = append(actions, action{h.Time, opReopen, h.Issue, ""})
			case "labeled":
				actions = append(actions, action{h.Time, opLabel, h.Issue, h.Text})
			case "unlabeled":
				actions = append(actions, action{h.Time, opUnlabel, h.Issue, h.Text})
			}
			last = h.RowID
		}
	}
	sort.Stable(actionsByTime(actions))
	return actions, int(maxIssue)
}

type actionsByTime []action

func (x actionsByTime) Len() int           { return len(x) }
func (x actionsByTime) Swap(i, j int)      { x[i], x[j] = x[j], x[i] }
func (x actionsByTime) Less(i, j int) bool { return x[i].time < x[j].time }

func plot(actions []action, maxIssue int, emit func([]issueState, string)) {
	var lastTime string
	state := make([]issueState, maxIssue+1)
	for _, a := range actions {
		thisTime := a.time[:10]
		if thisTime != lastTime {
			if lastTime != "" {
				emit(state, lastTime)
			}
			lastTime = thisTime
		}
		s := &state[a.number]
		switch a.op {
		case opCreate:
			s.createTime = a.time
		case opMilestone:
			s.milestone = a.text
		case opDemilestone:
			if s.milestone == a.text {
				s.milestone = ""
			}
		case opClose:
			s.closeTime = a.time
		case opReopen:
			s.closeTime = ""
		case opLabel, opUnlabel:
			var setting *bool
			switch a.text {
			case "NeedsInvestigation":
				setting = &s.needsInvestigation
			case "NeedsFix":
				setting = &s.needsFix
			case "NeedsDecision":
				setting = &s.needsDecision
			case "WaitingForInfo":
				setting = &s.waitingForInfo
			}
			if setting != nil {
				*setting = a.op == opLabel
			}
		}
	}
	if lastTime != "" {
		emit(state, lastTime)
	}
}

const minDate = "2016-04-01"

func dash() {
	actions, maxIssue := dashActions()
	plotRelease(actions, maxIssue, "Go1.8")
	plotRelease(actions, maxIssue, "Go1.9")
	plotNeeds(actions, maxIssue)
}

func plotRelease(actions []action, maxIssue int, release string) {
	releaseEarly := release + "Early"
	releaseMaybe := release + "Maybe"

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "var %sData = [", strings.Replace(release, ".", "", -1))
	fmt.Fprintf(&buf, "  ['Date', '%s', '%s', '%s', 'No Milestone']", releaseEarly, release, releaseMaybe)
	plot(actions, maxIssue, func(issues []issueState, time string) {
		if time < minDate {
			return
		}
		var numNone, numRelease, numReleaseEarly, numReleaseMaybe int
		for id := range issues {
			issue := &issues[id]
			if issue.createTime == "" || issue.closeTime != "" {
				continue
			}
			switch issue.milestone {
			case "":
				numNone++
			case release:
				numRelease++
			case releaseEarly:
				numReleaseEarly++
			case releaseMaybe:
				numReleaseMaybe++
			}
		}
		fmt.Fprintf(&buf, ",\n  [myDate(\"%s\"), %d, %d, %d, %d]", time, numNone, numReleaseEarly, numRelease, numReleaseMaybe)
	})
	fmt.Fprintf(&buf, "\n];\n\n")
	os.Stdout.Write(buf.Bytes())
}

func plotNeeds(actions []action, maxIssue int) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "var TriageData = [")
	fmt.Fprintf(&buf, "  ['Date', 'Triage', 'NeedsInvestigation', 'NeedsInvestigation+Waiting', 'NeedsInvestigation+Blocked',  'NeedsDecision', 'NeedsDecision+Waiting', 'NeedsDecision+Blocked',  'NeedsFix', 'NeedsFix+Waiting', 'NeedsFix+Blocked']")
	plot(actions, maxIssue, func(issues []issueState, time string) {
		if time < minDate {
			return
		}
		const (
			triage = iota
			needsInvestigation
			needsInvestigationWaitingForInfo
			needsInvestigationBlocked
			needsDecision
			needsDecisionWaitingForInfo
			needsDecisionBlocked
			needsFix
			needsFixWaitingForInfo
			needsFixBlocked
			maxCount
		)
		var count [maxCount]int
		for id := range issues {
			issue := &issues[id]
			if issue.createTime == "" || issue.closeTime != "" {
				continue
			}
			if issue.milestone != "" && !strings.HasPrefix(issue.milestone, "Go1.8") {
				continue
			}
			ix := triage
			switch {
			case issue.needsInvestigation:
				ix = needsInvestigation
			case issue.needsDecision:
				ix = needsDecision
			case issue.needsFix:
				ix = needsFix
			}
			if ix != triage {
				if issue.waitingForInfo {
					ix += 1
				} else if issue.blocked {
					ix += 2
				}
			}
			count[ix]++
		}
		fmt.Fprintf(&buf, ",\n  [myDate(\"%s\")", time)
		for _, x := range count {
			fmt.Fprintf(&buf, ", %d", x)
		}
		fmt.Fprintf(&buf, "]")
	})
	fmt.Fprintf(&buf, "\n];\n\n")
	os.Stdout.Write(buf.Bytes())
}
