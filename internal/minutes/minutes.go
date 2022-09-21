// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Minutes is the program we use to post the proposal review minutes.
// It is a demonstration of the use of the rsc.io/github API, but it is also not great code,
// which is why it is buried in an internal directory.
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"rsc.io/github"
)

func main() {
	r, err := NewReporter()
	if err != nil {
		log.Fatal(err)
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	r.Print(r.Update(string(data)))
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

func (r *Reporter) Update(text string) *Minutes {
	const prefix = "https://github.com/golang/go/issues/"

	m := new(Minutes)
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.ReplaceAll(line, "\t", " ")
		if line == "" {
			continue
		}
		if m.Who == nil {
			if strings.HasPrefix(line, prefix) {
				log.Printf("missing attendee list at start of input")
				break
			}
			who := strings.Fields(strings.ReplaceAll(line, ",", " "))
			for i, w := range who {
				who[i] = gitWho(w)
			}
			m.Who = who
			continue
		}

		if !strings.HasPrefix(line, prefix) {
			log.Printf("unexpected line: %s", line)
			continue
		}

		url, actionstr, _ := strings.Cut(line, " ")
		issuenum := strings.TrimPrefix(url, prefix)
		url = "https://go.dev/issue/" + issuenum
		actionstr = strings.TrimSpace(actionstr)
		if actionstr == "" {
			log.Printf("line missing actions: %s", line)
			continue
		}

		actions := strings.Split(actionstr, ";")
		col := "Active"
		reason := ""
		for i, a := range actions {
			a = strings.TrimSpace(a)
			actions[i] = a
			switch a {
			case "accept":
				a = "accepted"
			case "decline":
				a = "declined"
			case "retract":
				a = "retracted"
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
			if strings.HasPrefix(a, "duplicate") {
				col = "Declined"
				reason = "duplicate"
			}
			if strings.HasPrefix(a, "infeasible") {
				col = "Declined"
				reason = "infeasible"
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

		id, err := strconv.Atoi(issuenum)
		if err != nil {
			log.Fatal(err)
		}
		item := r.Items[id]
		if item == nil {
			log.Printf("missing from proposal project: #%d", id)
			continue
		}
		issue := item.Issue
		status := item.FieldByName("Status")
		if status == nil {
			log.Printf("item missing status: #%d", id)
			continue
		}

		title := strings.TrimSpace(strings.TrimPrefix(issue.Title, "proposal:"))
		if status.Option.Name != col {
			msg := updateMsg(status.Option.Name, col, reason)
			if msg == "" {
				log.Fatalf("no update message for %s", col)
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

		forceClose := func() {
			if !issue.Closed {
				if err := r.Client.CloseIssue(issue); err != nil {
					log.Printf("%s: closing issue: %v", url, err)
				}
			}
		}

		switch col {
		case "Accepted":
			if strings.HasPrefix(issue.Title, "proposal:") {
				if err := r.Client.RetitleIssue(issue, title); err != nil {
					log.Printf("%s: retitling: %v", url, err)
				}
			}
			needLabel("Proposal-Accepted")
			if issue.Milestone == nil || issue.Milestone.Title == "Proposal" {
				if err := r.Client.RemilestoneIssue(issue, r.Backlog); err != nil {
					log.Printf("%s: moving out of Proposal milestone: %v", url, err)
				}
			}
		case "Declined":
			dropLabel("Proposal-FinalCommentPeriod")
			forceClose()
		case "Likely Accept", "Likely Decline":
			needLabel("Proposal-FinalCommentPeriod")
		case "Hold":
			needLabel("Proposal-Hold")
		}
		m.Events = append(m.Events, &Event{Column: col, Issue: issuenum, Title: title, Actions: actions})
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
