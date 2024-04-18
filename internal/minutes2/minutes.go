// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Minutes is the program we use to post the proposal review minutes.
// It is a demonstration of the use of the rsc.io/github API, but it is also not great code,
// which is why it is buried in an internal directory.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"rsc.io/github"
)

var docjson = flag.Bool("docjson", false, "print google doc info in json")

func main() {
	flag.Parse()

	doc := parseDoc()
	if *docjson {
		js, err := json.MarshalIndent(doc, "", "\t")
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(append(js, '\n'))
		return
	}

	r, err := NewReporter()
	if err != nil {
		log.Fatal(err)
	}
	r.RetireOld()

	r.Print(r.Update(doc))
}

type Reporter struct {
	Client    *github.Client
	Proposals *github.Project
	Items     map[int]*github.ProjectItem
	Labels    map[string]*github.Label
	Backlog   *github.Milestone
}

func NewReporter() (*Reporter, error) {
	c, err := github.Dial("")
	if err != nil {
		return nil, err
	}

	r := &Reporter{Client: c}

	ps, err := r.Client.Projects("golang", "")
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		if p.Title == "Proposals" {
			r.Proposals = p
			break
		}
	}
	if r.Proposals == nil {
		return nil, fmt.Errorf("cannot find Proposals project")
	}

	labels, err := r.Client.SearchLabels("golang", "go", "")
	if err != nil {
		return nil, err
	}
	r.Labels = make(map[string]*github.Label)
	for _, label := range labels {
		r.Labels[label.Name] = label
	}

	milestones, err := r.Client.SearchMilestones("golang", "go", "Backlog")
	if err != nil {
		return nil, err
	}
	for _, m := range milestones {
		if m.Title == "Backlog" {
			r.Backlog = m
			break
		}
	}
	if r.Backlog == nil {
		return nil, fmt.Errorf("cannot find Backlog milestone")
	}

	items, err := r.Client.ProjectItems(r.Proposals)
	if err != nil {
		return nil, err
	}
	r.Items = make(map[int]*github.ProjectItem)
	for _, item := range items {
		if item.Issue == nil {
			log.Printf("warning: unexpected item with no issue")
			continue
		}
		r.Items[item.Issue.Number] = item
	}

	return r, nil
}

type Minutes struct {
	Who    []string
	Events []*Event
}

type Event struct {
	Column  string
	Issue   string
	Title   string
	Actions []string
}

const checkQuestion = "Have all remaining concerns about this proposal been addressed?"

func (r *Reporter) Update(doc *Doc) *Minutes {
	const prefix = "https://github.com/golang/go/issues/"

	m := new(Minutes)

	// Attendees
	if len(doc.Who) == 0 {
		log.Fatalf("missing attendees")
	}
	m.Who = make([]string, len(doc.Who))
	for i, w := range doc.Who {
		m.Who[i] = gitWho(w)
	}
	sort.Strings(m.Who)

	seen := make(map[int]bool)
Issues:
	for _, di := range doc.Issues {
		item := r.Items[di.Number]
		if item == nil {
			log.Printf("missing from proposal project: #%d", di.Number)
			continue
		}
		seen[di.Number] = true
		issue := item.Issue
		status := item.FieldByName("Status")
		if status == nil {
			log.Printf("item missing status: #%d", di.Number)
			continue
		}

		title := strings.TrimSpace(strings.TrimPrefix(issue.Title, "proposal:"))
		if title != di.Title {
			log.Printf("#%d title mismatch:\nGH: %s\nDoc: %s", di.Number, issue.Title, di.Title)
		}

		url := "https://go.dev/issue/" + fmt.Sprint(di.Number)
		actions := strings.Split(di.Minutes, ";")
		col := "Active"
		reason := ""
		check := false
		for i, a := range actions {
			a = strings.TrimSpace(a)
			actions[i] = a
			switch a {
			case "TODO":
				log.Printf("%s: minutes TODO", url)
				continue Issues
			case "accept":
				a = "accepted"
			case "decline":
				a = "declined"
			case "retract":
				a = "retracted"
			case "declined as infeasible":
				a = "infeasible"
			case "check":
				check = true
				a = "comment"
			}

			switch a {
			case "likely accept":
				col = "Likely Accept"
			case "likely decline":
				col = "Likely Decline"
			case "accepted":
				col = "Accepted"
			case "declined":
				col = "Declined"
			case "retracted":
				col = "Declined"
				reason = "retracted"
			case "unhold":
				col = "Active"
				reason = "unhold"
			}
			if strings.HasPrefix(a, "declined") {
				col = "Declined"
			}
			if strings.HasPrefix(a, "duplicate") {
				col = "Declined"
				reason = "duplicate"
			}
			if strings.Contains(a, "infeasible") {
				col = "Declined"
				reason = "infeasible"
			}
			if a == "obsolete" || strings.Contains(a, "obsoleted") {
				col = "Declined"
				reason = "obsolete"
			}
			if strings.HasPrefix(a, "closed") {
				col = "Declined"
			}
			if strings.HasPrefix(a, "hold") || a == "on hold" {
				col = "Hold"
			}
			if r := actionMap[a]; r != "" {
				actions[i] = r
			}
			if strings.HasPrefix(a, "removed") {
				col = "none"
				reason = "removed"
			}
		}

		if check {
			comments, err := r.Client.IssueComments(issue)
			if err != nil {
				log.Printf("%s: cannot read issue comments\n", url)
				continue
			}
			for i := len(comments) - 1; i >= 0; i-- {
				c := comments[i]
				if time.Since(c.CreatedAt) < 5*24*time.Hour && strings.Contains(c.Body, checkQuestion) {
					log.Printf("%s: recently checked", url)
					continue Issues
				}
			}

			if di.Details == "" {
				log.Printf("%s: missing proposal details", url)
				continue Issues
			}
			msg := fmt.Sprintf("%s\n\n%s", checkQuestion, di.Details)
			// log.Fatalf("wouldpost %s\n%s", url, msg)
			if err := r.Client.AddIssueComment(issue, msg); err != nil {
				log.Printf("%s: posting comment: %v", url, err)
			}
			log.Printf("posted %s", url)
		}

		if status.Option.Name != col {
			msg := updateMsg(status.Option.Name, col, reason)
			if msg == "" {
				log.Fatalf("no update message for %s", col)
			}
			if col == "Likely Accept" || col == "Accepted" {
				if di.Details == "" {
					log.Printf("%s: missing proposal details", url)
					continue Issues
				}
				msg += "\n\n" + di.Details
			}
			f := r.Proposals.FieldByName("Status")
			if col == "none" {
				if err := r.Client.DeleteProjectItem(r.Proposals, item); err != nil {
					log.Printf("%s: deleting proposal item: %v", url, err)
					continue
				}
			} else {
				o := f.OptionByName(col)
				if o == nil {
					log.Printf("%s: moving from %s to %s: no such status\n", url, status.Option.Name, col)
					continue
				}
				if err := r.Client.SetProjectItemFieldOption(r.Proposals, item, f, o); err != nil {
					log.Printf("%s: moving from %s to %s: %v\n", url, status.Option.Name, col, err)
				}
			}
			if err := r.Client.AddIssueComment(issue, msg); err != nil {
				log.Printf("%s: posting comment: %v", url, err)
			}
		}

		needLabel := func(name string) {
			if issue.LabelByName(name) == nil {
				lab := r.Labels[name]
				if lab == nil {
					log.Fatalf("%s: cannot find label %s", url, name)
				}
				if err := r.Client.AddIssueLabels(issue, lab); err != nil {
					log.Printf("%s: adding %s: %v", url, name, err)
				}
			}
		}

		dropLabel := func(name string) {
			if lab := issue.LabelByName(name); lab != nil {
				if err := r.Client.RemoveIssueLabels(issue, lab); err != nil {
					log.Printf("%s: removing %s: %v", url, name, err)
				}
			}
		}

		setLabel := func(name string, val bool) {
			if val {
				needLabel(name)
			} else {
				dropLabel(name)
			}
		}

		forceClose := func() {
			if !issue.Closed {
				if err := r.Client.CloseIssue(issue); err != nil {
					log.Printf("%s: closing issue: %v", url, err)
				}
			}
		}

		if col == "Accepted" {
			if strings.HasPrefix(issue.Title, "proposal:") {
				if err := r.Client.RetitleIssue(issue, title); err != nil {
					log.Printf("%s: retitling: %v", url, err)
				}
			}
			if issue.Milestone == nil || issue.Milestone.Title == "Proposal" {
				if err := r.Client.RemilestoneIssue(issue, r.Backlog); err != nil {
					log.Printf("%s: moving out of Proposal milestone: %v", url, err)
				}
			}
		}
		if col == "Declined" {
			forceClose()
		}

		setLabel("Proposal-Accepted", col == "Accepted")
		setLabel("Proposal-FinalCommentPeriod", col == "Likely Accept" || col == "Likely Decline")
		setLabel("Proposal-Hold", col == "Hold")

		m.Events = append(m.Events, &Event{Column: col, Issue: fmt.Sprint(di.Number), Title: title, Actions: actions})
	}

	for id, item := range r.Items {
		status := item.FieldByName("Status")
		if status != nil {
			switch status.Option.Name {
			case "Active", "Likely Accept", "Likely Decline":
				if !seen[id] {
					log.Printf("#%d: missing from doc", id)
				}
			}
		}
	}

	sort.Slice(m.Events, func(i, j int) bool {
		return m.Events[i].Title < m.Events[j].Title
	})
	return m
}

func (r *Reporter) Print(m *Minutes) {
	fmt.Printf("**%s / ", time.Now().Format("2006-01-02"))
	for i, who := range m.Who {
		if i > 0 {
			fmt.Printf(", ")
		}
		fmt.Printf("%s", who)
	}
	fmt.Printf("**\n\n")

	disc, err := r.Client.Discussions("golang", "go")
	if err != nil {
		log.Fatal(err)
	}
	first := true
	for _, d := range disc {
		if d.Locked {
			continue
		}
		if first {
			fmt.Printf("**Discussions (not yet proposals)**\n\n")
			first = false
		}
		fmt.Printf("- **%s** [#%d](https://go.dev/issue/%d)\n", markdownEscape(strings.TrimSpace(d.Title)), d.Number, d.Number)
	}
	if !first {
		fmt.Printf("\n")
	}

	columns := []string{
		"Accepted",
		"Declined",
		"Likely Accept",
		"Likely Decline",
		"Active",
		"Hold",
		"Other",
	}

	for _, col := range columns {
		n := 0
		for i, e := range m.Events {
			if e == nil || e.Column != col && col != "Other" {
				continue
			}
			if n == 0 {
				fmt.Printf("**%s**\n\n", col)
			}
			n++
			fmt.Printf("- **%s** [#%s](https://go.dev/issue/%s)\n", markdownEscape(strings.TrimSpace(e.Title)), e.Issue, e.Issue)
			for _, a := range e.Actions {
				fmt.Printf("  - %s\n", a)
			}
			m.Events[i] = nil
		}
		if n == 0 && col != "Hold" && col != "Other" {
			fmt.Printf("**%s**\n\n", col)
			fmt.Printf("- none\n")
		}
		fmt.Printf("\n")
	}
}

var markdownEscaper = strings.NewReplacer(
	"_", `\_`,
	"*", `\*`,
	"`", "\\`",
	"[", `\[`,
)

func markdownEscape(s string) string {
	return markdownEscaper.Replace(s)
}

func (r *Reporter) RetireOld() {
	for _, item := range r.Items {
		issue := item.Issue
		if issue.Closed && !issue.ClosedAt.IsZero() && time.Since(issue.ClosedAt) > 365*24*time.Hour {
			log.Printf("retire #%d", issue.Number)
			if err := r.Client.DeleteProjectItem(r.Proposals, item); err != nil {
				log.Printf("#%d: deleting proposal item: %v", issue.Number, err)
			}
		}
	}
}
