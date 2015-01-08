// Copyright 2013 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Originally code.google.com/p/rsc/cmd/issue/acme.go.

package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"

	"code.google.com/p/goplan9/draw"
	"code.google.com/p/goplan9/plan9/acme"
)

func acmeMode() {
	var dummy awin
	dummy.prefix = "/issue/" + *project + "/"
	if flag.NArg() > 0 {
		for _, arg := range flag.Args() {
			if !dummy.look(arg) {
				dummy.newSearch("search", arg)
			}
		}
	} else {
		dummy.look("all")
	}
	select {}
}

const (
	modeSingle = 1 + iota
	modeQuery
	modeCreate
	modeMilestone
)

type awin struct {
	*acme.Win
	prefix   string
	mode     int
	query    string
	id       int
	github   *github.Issue
	tab      int
	font     *draw.Font
	fontName string
	title    string
}

var all struct {
	sync.Mutex
	m      map[string]*awin
	f      map[string]*draw.Font
	numwin int
}

func (w *awin) exit() {
	all.Lock()
	defer all.Unlock()
	if all.m[w.title] == w {
		delete(all.m, w.title)
	}
	if all.numwin--; all.numwin == 0 {
		os.Exit(0)
	}
}

func (w *awin) new(title string) *awin {
	all.Lock()
	defer all.Unlock()
	all.numwin++
	if all.m == nil {
		all.m = make(map[string]*awin)
	}
	w1 := new(awin)
	w1.title = title
	var err error
	w1.Win, err = acme.New()
	if err != nil {
		log.Printf("creating acme window: %v", err)
		time.Sleep(10 * time.Millisecond)
		w1.Win, err = acme.New()
		if err != nil {
			log.Fatalf("creating acme window again: %v", err)
		}
	}
	w1.prefix = w.prefix
	w1.Name(w1.prefix + title)
	if title != "new" {
		all.m[title] = w1
	}
	return w1
}

func (w *awin) show(title string) *awin {
	all.Lock()
	defer all.Unlock()
	if w1 := all.m[title]; w1 != nil {
		w.Ctl("show")
		return w1
	}
	return nil
}

func (w *awin) fixfont() {
	ctl := make([]byte, 1000)
	w.Seek("ctl", 0, 0)
	n, err := w.Read("ctl", ctl)
	if err != nil {
		return
	}
	f := strings.Fields(string(ctl[:n]))
	if len(f) < 8 {
		return
	}
	w.tab, _ = strconv.Atoi(f[7])
	if w.tab == 0 {
		return
	}
	name := f[6]
	if w.fontName == name {
		return
	}
	all.Lock()
	defer all.Unlock()
	if font := all.f[name]; font != nil {
		w.font = font
		w.fontName = name
		return
	}
	var disp *draw.Display = nil
	font, err := disp.OpenFont(name)
	if err != nil {
		return
	}
	if all.f == nil {
		all.f = make(map[string]*draw.Font)
	}
	all.f[name] = font
	w.font = font
}

var numRE = regexp.MustCompile(`(?m)^#[0-9]+\t`)

var milecache struct {
	sync.Mutex
	list []github.Milestone
}

func cachedMilestones() []github.Milestone {
	milecache.Lock()
	if milecache.list == nil {
		milecache.list, _ = loadMilestones()
	}
	list := milecache.list
	milecache.Unlock()
	return list
}

func (w *awin) look(text string) bool {
	if text == "all" {
		if w.show("all") != nil {
			return true
		}
		w.newSearch("all", "")
		return true
	}
	if text == "Milestone" || text == "Milestones" || text == "milestone" {
		if w.show("milestone") != nil {
			return true
		}
		w.newMilestoneList()
		return true
	}
	milecache.Lock()
	if milecache.list == nil {
		milecache.list, _ = loadMilestones()
	}
	list := milecache.list
	milecache.Unlock()
	for _, m := range list {
		if getString(m.Title) == text {
			if w.show(text) != nil {
				return true
			}
			w.newSearch(text, "milestone:"+text)
			return true
		}
	}

	if n, _ := strconv.Atoi(strings.TrimPrefix(text, "#")); 0 < n && n < 100000 {
		text = strings.TrimPrefix(text, "#")
		if w.show(text) != nil {
			return true
		}
		w.newIssue(text, n)
		return true
	}
	if m := numRE.FindAllString(text, -1); m != nil {
		for _, s := range m {
			w.look(strings.TrimSpace(strings.TrimPrefix(s, "#")))
		}
		return true
	}
	return false
}

func (w *awin) setMilestone(milestone, text string) {
	id := findMilestone(w, &milestone)
	if id == nil {
		return
	}
	milestoneID := *id

	stop := w.blinker()
	defer stop()
	if w.mode == modeSingle {
		w.setMilestone1(milestoneID, w.id)
		w.load()
		return
	}
	if n, _ := strconv.Atoi(strings.TrimPrefix(text, "#")); 0 < n && n < 100000 {
		w.setMilestone1(milestoneID, n)
		return
	}
	if m := numRE.FindAllString(text, -1); m != nil {
		for _, s := range m {
			n, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "#")))
			if 0 < n && n < 100000 {
				w.setMilestone1(milestoneID, n)
			}
		}
		return
	}
}

func (w *awin) setMilestone1(milestoneID, n int) {
	var edit github.IssueRequest
	edit.Milestone = &milestoneID

	_, _, err := client.Issues.Edit(projectOwner, projectRepo, n, &edit)
	if err != nil {
		w.err(fmt.Sprintf("Error changing issue #%d: %v", n, err))
	}
}

func (w *awin) createIssue() {
	w = w.new("new")
	w.mode = modeCreate
	w.Ctl("cleartag")
	w.Fprintf("tag", " Put Search ")
	go w.load()
	go w.loop()
}

func (w *awin) newIssue(title string, id int) {
	w = w.new(title)
	w.mode = modeSingle
	w.id = id
	w.Ctl("cleartag")
	w.Fprintf("tag", " Get Put Look ")
	go w.load()
	go w.loop()
}

func (w *awin) newMilestoneList() {
	w = w.new("milestone")
	w.mode = modeMilestone
	w.query = ""
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

func (w *awin) newSearch(title, query string) {
	w = w.new(title)
	w.mode = modeQuery
	w.query = query
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

func (w *awin) blinker() func() {
	c := make(chan struct{})
	go func() {
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		dirty := false
		for {
			select {
			case <-t.C:
				dirty = !dirty
				if dirty {
					w.Ctl("dirty")
				} else {
					w.Ctl("clean")
				}
			case <-c:
				if dirty {
					w.Ctl("clean")
				}
				c <- struct{}{}
				return
			}
		}
	}()
	return func() {
		c <- struct{}{}
		<-c
	}
}

func (w *awin) clear() {
	w.Addr(",")
	w.Write("data", nil)
}

var createTemplate = `Title: 
Assignee: 
Labels: 
Milestone: 

<describe issue here>

`

func (w *awin) load() {
	w.fixfont()

	switch w.mode {
	case modeCreate:
		w.clear()
		w.Write("body", []byte(createTemplate))
		w.Ctl("clean")

	case modeSingle:
		var buf bytes.Buffer
		stop := w.blinker()
		issue, err := showIssue(&buf, w.id)
		stop()
		w.clear()
		if err != nil {
			w.Write("body", []byte(err.Error()))
			break
		}
		w.Write("body", buf.Bytes())
		w.Ctl("clean")
		w.github = issue

	case modeMilestone:
		stop := w.blinker()
		milestones, err := loadMilestones()
		milecache.Lock()
		milecache.list = milestones
		milecache.Unlock()
		stop()
		w.clear()
		if err != nil {
			w.Fprintf("body", "Error loading milestones: %v\n", err)
			break
		}
		var buf bytes.Buffer
		for _, m := range milestones {
			fmt.Fprintf(&buf, "%s\t%s\t%d\n", getTime(m.DueOn).Format("2006-01-02"), getString(m.Title), getInt(m.OpenIssues))
		}
		w.printTabbed(buf.String())
		w.Ctl("clean")

	case modeQuery:
		var buf bytes.Buffer
		stop := w.blinker()
		err := showQuery(&buf, w.query)
		if w.title == "all" {
			cachedMilestones()
		}
		stop()
		w.clear()
		if err != nil {
			w.Write("body", []byte(err.Error()))
			break
		}
		if w.title == "all" {
			var names []string
			for _, m := range cachedMilestones() {
				names = append(names, getString(m.Title))
			}
			if len(names) > 0 {
				w.Fprintf("body", "Milestones: %s\n\n", strings.Join(names, " "))
			}
		}
		if w.title == "search" {
			w.Fprintf("body", "Search: %s\n\n", w.query)
		}
		w.printTabbed(buf.String())
		w.Ctl("clean")
	}

	w.Addr("0")
	w.Ctl("dot=addr")
	w.Ctl("show")
}

func (w *awin) err(s string) {
	if !strings.HasSuffix(s, "\n") {
		s = s + "\n"
	}
	w1 := w.show("+Errors")
	if w1 == nil {
		w1 = w.new("+Errors")
	}
	w1.Fprintf("body", "%s", s)
	w1.Addr("$")
	w1.Ctl("dot=addr")
	w1.Ctl("show")
}

func diff(line, field, old string) *string {
	old = strings.TrimSpace(old)
	line = strings.TrimSpace(strings.TrimPrefix(line, field))
	if old == line {
		return nil
	}
	return &line
}

func diffList(line, field string, old []string) []string {
	line = strings.TrimSpace(strings.TrimPrefix(line, field))
	had := make(map[string]bool)
	for _, f := range old {
		had[f] = true
	}
	changes := false
	for _, f := range strings.Fields(line) {
		if !had[f] {
			changes = true
		}
		delete(had, f)
	}
	if len(had) != 0 {
		changes = true
	}
	if changes {
		ret := strings.Fields(line)
		if ret == nil {
			ret = []string{}
		}
		return ret
	}
	return nil
}

func (w *awin) put() {
	stop := w.blinker()
	defer stop()
	switch w.mode {
	case modeSingle, modeCreate:
		old := w.github
		if w.mode == modeCreate {
			old = new(github.Issue)
		}
		data, err := w.ReadAll("body")
		if err != nil {
			w.err(fmt.Sprintf("Put: %v", err))
			return
		}
		sdata := string(data)
		off := 0
		var edit github.IssueRequest
		for _, line := range strings.SplitAfter(sdata, "\n") {
			off += len(line)
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			switch {
			case strings.HasPrefix(line, "#"):
				continue

			case strings.HasPrefix(line, "Title:"):
				edit.Title = diff(line, "Title:", getString(old.Title))

			case strings.HasPrefix(line, "State:"):
				edit.State = diff(line, "State:", getString(old.State))

			case strings.HasPrefix(line, "Assignee:"):
				edit.Assignee = diff(line, "Assignee:", getUserLogin(old.Assignee))

			case strings.HasPrefix(line, "Closed:"):
				continue

			case strings.HasPrefix(line, "Labels:"):
				edit.Labels = diffList(line, "Labels:", getLabelNames(old.Labels))

			case strings.HasPrefix(line, "Milestone:"):
				edit.Milestone = findMilestone(w, diff(line, "Milestone:", getMilestoneTitle(old.Milestone)))

			case strings.HasPrefix(line, "URL:"):
				continue

			default:
				w.err(fmt.Sprintf("Put: unknown summary line: %s", line))
			}
		}

		if w.mode == modeCreate {
			comment := strings.TrimSpace(sdata[off:])
			edit.Body = &comment
			issue, _, err := client.Issues.Create(projectOwner, projectRepo, &edit)
			if err != nil {
				w.err(fmt.Sprintf("Error creating issue: %v", err))
				return
			}
			w.mode = modeSingle
			w.id = getInt(issue.Number)
			w.title = fmt.Sprint(w.id)
			w.Name(w.prefix + w.title)
			all.Lock()
			all.m[w.title] = w
			all.Unlock()
			w.github = issue
			w.load()
			return
		}

		var comment string
		i := strings.Index(sdata, "\nReported by ")
		if i >= off {
			comment = strings.TrimSpace(sdata[off:i])
		}

		failed := false
		if comment != "" {
			_, _, err := client.Issues.CreateComment(projectOwner, projectRepo, getInt(old.Number), &github.IssueComment{
				Body: &comment,
			})
			if err != nil {
				w.err(fmt.Sprintf("Error saving comment: %v", err))
				failed = true
			}
		}

		if edit.Title != nil || edit.State != nil || edit.Assignee != nil || edit.Labels != nil || edit.Milestone != nil {
			_, _, err := client.Issues.Edit(projectOwner, projectRepo, getInt(old.Number), &edit)
			if err != nil {
				w.err(fmt.Sprintf("Error changing issue: %v", err))
				if !failed {
					w.err("(Comment saved; only metadata failed to update.)\n")
				}
				failed = true
			} else if failed {
				w.err("(Metadata changes made; only comment failed to save.)\n")
			}
		}

		if !failed {
			w.load()
		}

	case modeMilestone:
		w.err("cannot Put milestone list")

	case modeQuery:
		w.err("cannot Put issue list")
	}
}

func (w *awin) loadText(e *acme.Event) {
	if len(e.Text) == 0 && e.Q0 < e.Q1 {
		w.Addr("#%d,#%d", e.Q0, e.Q1)
		data, err := w.ReadAll("xdata")
		if err != nil {
			w.err(err.Error())
		}
		e.Text = data
	}
}

func (w *awin) selection() string {
	w.Ctl("addr=dot")
	data, err := w.ReadAll("xdata")
	if err != nil {
		w.err(err.Error())
	}
	return string(data)
}

func (w *awin) loop() {
	defer w.exit()
	for e := range w.EventChan() {
		switch e.C2 {
		case 'x', 'X': // execute
			cmd := strings.TrimSpace(string(e.Text))
			if cmd == "Get" {
				w.load()
				break
			}
			if cmd == "Put" {
				w.put()
				break
			}
			if cmd == "Del" {
				w.Ctl("del")
				break
			}
			if cmd == "New" {
				w.createIssue()
				break
			}
			if strings.HasPrefix(cmd, "Search ") {
				w.newSearch("search", strings.TrimSpace(strings.TrimPrefix(cmd, "Search")))
				break
			}
			if strings.HasPrefix(cmd, "Milestone ") {
				text := w.selection()
				w.setMilestone(strings.TrimSpace(strings.TrimPrefix(cmd, "Milestone")), text)
				break
			}
			w.WriteEvent(e)
		case 'l', 'L': // look
			w.loadText(e)
			if !w.look(string(e.Text)) {
				w.WriteEvent(e)
			}
		}
	}
}

func (w *awin) printTabbed(text string) {
	lines := strings.SplitAfter(text, "\n")
	var rows [][]string
	for _, line := range lines {
		if line == "" {
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		rows = append(rows, strings.Split(line, "\t"))
	}

	var wid []int

	if w.font != nil {
		for _, row := range rows {
			for len(wid) < len(row) {
				wid = append(wid, 0)
			}
			for i, col := range row {
				n := w.font.StringWidth(col)
				if wid[i] < n {
					wid[i] = n
				}
			}
		}
	}

	var buf bytes.Buffer
	for _, row := range rows {
		for i, col := range row {
			buf.WriteString(col)
			if i == len(row)-1 {
				break
			}
			if w.font == nil || w.tab == 0 {
				buf.WriteString("\t")
				continue
			}
			pos := w.font.StringWidth(col)
			for pos <= wid[i] {
				buf.WriteString("\t")
				pos += w.tab - pos%w.tab
			}
		}
		buf.WriteString("\n")
	}

	w.Write("body", buf.Bytes())
}

func findMilestone(w *awin, name *string) *int {
	if name == nil {
		return nil
	}

	all, err := loadMilestones()
	if err != nil {
		w.err(fmt.Sprintf("Error loading milestone list: %v\n\tIgnoring milestone change.\n", err))
		return nil
	}

	for _, m := range all {
		if getString(m.Title) == *name {
			return m.Number
		}
	}

	w.err(fmt.Sprintf("Ignoring unknown milestone: %s\n", *name))
	return nil
}
