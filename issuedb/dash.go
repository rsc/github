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
	"strconv"
	"strings"
	"time"
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

func dashActions(proj string) ([]action, int) {
	var actions []action
	var maxIssue int64
	rows, err := db.Query("select * from History where Project = ? order by Time", proj)
	if err != nil {
		log.Fatal("sql: %v", err)
	}
	for rows.Next() {
		var h History
		if err := rows.Scan(&h.URL, &h.Project, &h.Issue, &h.Time, &h.Who, &h.Action, &h.Text); err != nil {
			log.Fatal("sql scan History: %v", err)
		}
		if maxIssue < h.Issue {
			maxIssue = h.Issue
		}
		switch h.Action {
		case "issue":
			actions = append(actions, action{h.Time, opCreate, h.Issue, ""})
		case "milestone?", "milestoned":
			if h.Text != "" {
				actions = append(actions, action{h.Time, opMilestone, h.Issue, h.Text})
			}
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
	actions, maxIssue := dashActions("golang/go")
	plotRelease(actions, maxIssue, "Go1.8")
	plotRelease(actions, maxIssue, "Go1.9")
	plotNeeds(actions, maxIssue)
	plotActivity()
}

func plotRelease(actions []action, maxIssue int, release string) {
	releaseEarly := release + "Early"
	releaseMaybe := release + "Maybe"

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "var %sData = [", strings.Replace(release, ".", "", -1))
	fmt.Fprintf(&buf, "  ['Date', 'No Milestone', '%s', '%s', '%s']", releaseEarly, release, releaseMaybe)
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
				if time == "2016-10-05" {
					println("NONE", id)
				}
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

func plotActivity() {
	rows, err := db.Query("select Who, count(*) from History where Time >= '2016-04-05' group by Who")
	if err != nil {
		log.Fatalf("sql activity: %v", err)
	}
	totalWho := map[string]int{}
	for rows.Next() {
		var who string
		var count int
		if err := rows.Scan(&who, &count); err != nil {
			log.Fatal("sql scan counts: %v", err)
		}
		totalWho[who] += count
	}

	var allWho []string
	for who := range totalWho {
		allWho = append(allWho, who)
	}
	sort.Slice(allWho, func(i, j int) bool {
		ti := totalWho[allWho[i]]
		tj := totalWho[allWho[j]]
		if ti != tj {
			return ti > tj
		}
		return allWho[i] < allWho[j]
	})

	if len(allWho) > 40 {
		allWho = allWho[:40]
	}
	plotActivityCounts("GithubActivityData", "", allWho)
	for _, action := range []string{"assigned", "closed", "comment", "labeled", "mentioned", "milestoned", "renamed", "subscribed"} {
		plotActivityCounts("GithubActivityData_"+action, " and Action = '"+action+"'", allWho)
	}
}

type weekActivity struct {
	week  string
	count map[string]int
}

func plotActivityCounts(name, cond string, allWho []string) {
	rows, err := db.Query("select strftime('%Y-%W', Time) as Week, Who, count(*) as N from History where Time >= '2016-08-01'" + cond + " group by Week, Who order by Week, Who")
	if err != nil {
		log.Fatalf("sql activity counts: %v", err)
	}
	thisWeek := ""
	var weeks []weekActivity
	for rows.Next() {
		var count int
		var week, who string
		if err := rows.Scan(&week, &who, &count); err != nil {
			log.Fatalf("sql scan activity: %v", err)
		}
		if thisWeek != week {
			weeks = append(weeks, weekActivity{week: week, count: map[string]int{}})
			thisWeek = week
		}
		w := &weeks[len(weeks)-1]
		w.count[who] += count
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "var %s = ", name)
	printActivity(&buf, allWho, weeks)
	os.Stdout.Write(buf.Bytes())
}

func printActivity(buf *bytes.Buffer, allWho []string, weeks []weekActivity) {
	fmt.Fprintf(buf, "[\n")
	fmt.Fprintf(buf, "  ['Date'")
	for _, who := range allWho {
		fmt.Fprintf(buf, ", '%s'", who)
	}
	fmt.Fprintf(buf, "],\n")
	for _, w := range weeks {
		fmt.Fprintf(buf, " [%s", weekToDate(w.week))
		for _, who := range allWho {
			fmt.Fprintf(buf, ", %d", w.count[who])
		}
		fmt.Fprintf(buf, "],\n")
	}
	fmt.Fprintf(buf, "];\n\n")
}

func weekToDate(w string) string {
	y, err := strconv.Atoi(w[:4])
	if err != nil {
		log.Fatalf("bad week %s", w)
	}
	ww, err := strconv.Atoi(w[5:])
	if err != nil {
		log.Fatalf("bad week %s", w)
	}
	now := time.Date(y, time.January, 1, 12, 0, 0, 0, time.UTC)
	if ww > 0 {
		for now.Weekday() != time.Monday {
			now = now.AddDate(0, 0, 1)
		}
		now = now.AddDate(0, 0, (ww-1)*7)
	}
	return fmt.Sprintf("myDate('%s')", now.Format(time.RFC3339)[:10])
}
