// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rsc.io/todo/task"
)

type ghItem struct {
	Type    string
	URL     string
	Time    time.Time
	Issue   ghIssue
	Event   ghIssueEvent
	Comment ghIssueComment
}

const timeFormat = "2006-01-02 15:04:05 -0700"

func todo(proj *ProjectSync) {
	println("#", proj.Name)
	root := filepath.Join(os.Getenv("HOME"), "todo/github", filepath.Base(proj.Name))
	data, _ := ioutil.ReadFile(filepath.Join(root, "synctime"))
	var syncTime time.Time
	if len(data) > 0 {
		t, err := time.Parse(time.RFC3339, string(data))
		if err != nil {
			log.Fatalf("parsing %s: %v", filepath.Join(root, "synctime"), err)
		}
		syncTime = t
	}

	l := task.OpenList(root)

	// Start 10 minutes back just in case there is time skew in some way on GitHub.
	// (If this is not good enough, we can always impose our own sequence numbering
	// in the RawJSON table.)
	startTime := syncTime.Add(-10 * time.Minute)
	endTime := syncTime
	process(proj, startTime, func(proj *ProjectSync, issue int64, items []*ghItem) {
		fmt.Fprintf(os.Stderr, "%v#%v\n", proj.Name, issue)
		if end := items[len(items)-1].Time; endTime.Before(end) {
			endTime = end
		}
		todoIssue(l, proj, issue, items)
	})

	if err := ioutil.WriteFile(filepath.Join(root, "synctime"), []byte(endTime.Local().Format(time.RFC3339)), 0666); err != nil {
		log.Fatal(err)
	}
}

func todoIssue(l *task.List, proj *ProjectSync, issue int64, items []*ghItem) {
	id := fmt.Sprint(issue)
	t, err := l.Read(id)
	var last time.Time
	if err != nil {
		if items[0].Type != "/issues" {
			log.Printf("sync: missing creation for %v/%v", proj.Name, issue)
			return
		}
		it := &items[0].Issue
		last = items[0].Time
		hdr := map[string]string{
			"url":     it.HTMLURL,
			"author":  it.User.Login,
			"title":   it.Title,
			"updated": last.Format(timeFormat),
		}
		syncHdr(hdr, hdr, it)
		t, err = l.Create(id, items[0].Time.Local(), hdr, []byte(bodyText(it.User.Login, "reported", it.Body)))
		if err != nil {
			log.Fatal(err)
		}
		items = items[1:]
	} else {
		last, err = time.Parse(timeFormat, t.Header("updated"))
		if err != nil {
			log.Fatalf("sync: bad updated time in %v", issue)
		}
	}

	haveEID := make(map[string]bool)
	for _, eid := range t.EIDs() {
		haveEID[eid] = true
	}

	for _, it := range items {
		if last.Before(it.Time) {
			last = it.Time
		}
		h := sha256.Sum256([]byte(it.URL))
		eid := fmt.Sprintf("%x", h)[:8]
		if haveEID[eid] {
			continue
		}

		switch it.Type {
		default:
			log.Fatalf("unexpected type %s", it.Type)
		case "/issues":
			continue
		case "/issues/events":
			ev := &it.Event
			hdr := map[string]string{
				"#id":     eid,
				"updated": last.Local().Format(timeFormat),
			}
			what := "@" + ev.Actor.Login + " " + ev.Event
			switch ev.Event {
			case "closed", "merged", "referenced":
				what += ": " + "https://github.com/" + proj.Name + "/commit/" + ev.CommitID
				if ev.Event == "closed" || ev.Event == "merged" {
					hdr["closed"] = it.Time.Local().Format(time.RFC3339)
				}
			case "assigned", "unassigned":
				var list []string
				for _, who := range ev.Assignees {
					list = append(list, who.Login)
				}
				what += ": " + strings.Join(list, ", ")
				if ev.Event == "assigned" {
					hdr["assign"] = addList(t.Header("assign"), list)
				} else {
					hdr["assign"] = deleteList(t.Header("assign"), list)
				}
			case "labeled", "unlabeled":
				var list []string
				for _, lab := range ev.Labels {
					list = append(list, lab.Name)
				}
				what += ": " + strings.Join(list, ", ")
				if ev.Event == "labeled" {
					hdr["label"] = addList(t.Header("label"), list)
				} else {
					hdr["label"] = deleteList(t.Header("label"), list)
				}
			case "milestoned":
				what += ": " + ev.Milestone.Title
				hdr["milestone"] = ev.Milestone.Title
			case "demilestoned":
				hdr["milestone"] = ""
			case "renamed":
				what += ":\n\t" + ev.Rename.From + " →\n\t" + ev.Rename.To
			}
			if err := l.Write(t, it.Time.Local(), hdr, []byte(what)); err != nil {
				log.Fatal(err)
			}
		case "/issues/comments":
			com := &it.Comment
			hdr := map[string]string{
				"#id":     eid,
				"#url":    com.HTMLURL,
				"updated": last.Local().Format(timeFormat),
			}
			if err := l.Write(t, it.Time.Local(), hdr, []byte(bodyText(com.User.Login, "commented", com.Body))); err != nil {
				log.Fatal(err)
			}
		}
	}
}

func addList(old string, add []string) string {
	have := make(map[string]bool)
	for _, name := range strings.Split(old, ", ") {
		have[name] = true
	}
	for _, name := range add {
		if !have[name] {
			old += ", " + name
			have[name] = true
		}
	}
	return old
}

func deleteList(old string, del []string) string {
	drop := make(map[string]bool)
	for _, name := range del {
		drop[name] = true
	}
	var list []string
	for _, name := range strings.Split(old, ", ") {
		if name != "" && !drop[name] {
			list = append(list, name)
		}
	}
	return strings.Join(list, ", ")
}

func syncHdr(old, hdr map[string]string, it *ghIssue) {
	pr := ""
	if it.PullRequest != nil {
		pr = "pr"
	}
	if old["pr"] != pr {
		hdr["pr"] = pr
	}
	if old["milestone"] != it.Milestone.Title {
		hdr["milestone"] = it.Milestone.Title
	}
	locked := ""
	if it.Locked {
		locked := it.ActiveLockReason
		if locked == "" {
			locked = "locked"
		}
	}
	if old["locked"] != locked {
		hdr["locked"] = locked
	}
	closed := ""
	if it.ClosedAt != "" {
		closed = it.ClosedAt
	}
	if old["closed"] != closed {
		hdr["closed"] = closed
	}
	var list []string
	for _, who := range it.Assignees {
		list = append(list, who.Login)
	}
	all := strings.Join(list, ", ")
	if old["assign"] != all {
		hdr["assign"] = all
	}
	list = nil
	for _, lab := range it.Labels {
		list = append(list, lab.Name)
	}
	all = strings.Join(list, ", ")
	if old["label"] != all {
		hdr["label"] = all
	}
}

func process(proj *ProjectSync, since time.Time, do func(proj *ProjectSync, issue int64, item []*ghItem)) {
	rows, err := db.Query("select * from RawJSON where Project = ? and Time >= ? order by Issue, Time, Type", proj.Name, since.UTC().Format(time.RFC3339))
	if err != nil {
		log.Fatalf("sql: %v", err)
	}

	var items []*ghItem
	var lastIssue int64
	for rows.Next() {
		var raw RawJSON
		if err := rows.Scan(&raw.URL, &raw.Project, &raw.Issue, &raw.Type, &raw.JSON, &raw.Time); err != nil {
			log.Fatalf("sql scan RawJSON: %v", err)
		}
		if raw.Issue != lastIssue {
			if len(items) > 0 {
				do(proj, lastIssue, items)
			}
			items = items[:0]
			lastIssue = raw.Issue
		}

		var ev ghIssueEvent
		var com ghIssueComment
		var issue ghIssue
		switch raw.Type {
		default:
			log.Fatalf("unknown type %s", raw.Type)
		case "/issues/comments":
			err = json.Unmarshal(raw.JSON, &com)
		case "/issues/events":
			err = json.Unmarshal(raw.JSON, &ev)
		case "/issues":
			err = json.Unmarshal(raw.JSON, &issue)
		}
		if err != nil {
			log.Fatalf("unmarshal: %v", err)
		}
		tm, err := time.Parse(time.RFC3339, raw.Time)
		if err != nil {
			log.Fatalf("parse time: %v", err)
		}

		items = append(items, &ghItem{Type: raw.Type, URL: raw.URL, Time: tm, Issue: issue, Event: ev, Comment: com})
	}
	if len(items) > 0 {
		do(proj, lastIssue, items)
	}
}

func bodyText(who, verb, data string) []byte {
	body := "@" + who + " " + verb + ":\n"
	b := strings.Replace(data, "\r\n", "\n", -1)
	b = strings.TrimRight(b, "\n")
	b = strings.Replace(b, "\n", "\n\t", -1)
	body += "\n\t" + b
	return []byte(body)
}
