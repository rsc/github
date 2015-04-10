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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"9fans.net/go/acme"
	"9fans.net/go/draw"
	"github.com/google/go-github/github"
)

func acmeMode() {
	var dummy awin
	dummy.prefix = "/issue/" + *project + "/"
	if flag.NArg() > 0 {
		// TODO(rsc): Without -a flag, the query is conatenated into one query.
		// Decide which behavior should be used, and use it consistently.
		// TODO(rsc): Block this look from doing the multiline selection mode?
		for _, arg := range flag.Args() {
			if dummy.look(arg) {
				continue
			}
			if arg == "new" {
				dummy.createIssue()
				continue
			}
			dummy.newSearch("search", arg)
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
	modeBulk
)

type awin struct {
	*acme.Win
	prefix       string
	mode         int
	query        string
	id           int
	github       *github.Issue
	tab          int
	font         *draw.Font
	fontName     string
	title        string
	sortByNumber bool // otherwise sort by title
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
	ids := readBulkIDs([]byte(text))
	if len(ids) > 0 {
		for _, id := range ids {
			text := fmt.Sprint(id)
			if w.show(text) != nil {
				continue
			}
			w.newIssue(text, id)
		}
		return true
	}

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
	var buf bytes.Buffer
	id := findMilestone(&buf, &milestone)
	if buf.Len() > 0 {
		w.err(strings.TrimSpace(buf.String()))
	}
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

func (w *awin) newBulkEdit(body []byte) {
	w = w.new("bulk-edit/")
	w.mode = modeBulk
	w.query = ""
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Sort Search ")
	w.Write("body", append([]byte("Loading...\n\n"), body...))
	go w.load()
	go w.loop()
}

func (w *awin) newMilestoneList() {
	w = w.new("milestone")
	w.mode = modeMilestone
	w.query = ""
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Sort Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

func (w *awin) newSearch(title, query string) {
	w = w.new(title)
	w.mode = modeQuery
	w.query = query
	w.Ctl("cleartag")
	w.Fprintf("tag", " New Get Bulk Sort Search ")
	w.Write("body", []byte("Loading..."))
	go w.load()
	go w.loop()
}

func (w *awin) blinker() func() {
	c := make(chan struct{})
	go func() {
		t := time.NewTicker(1000 * time.Millisecond)
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
			w.Fprintf("body", "Search %s\n\n", w.query)
		}
		w.printTabbed(buf.String())
		w.Ctl("clean")

	case modeBulk:
		stop := w.blinker()
		body, err := w.ReadAll("body")
		if err != nil {
			w.err(fmt.Sprintf("%v", err))
			stop()
			break
		}
		base, original, err := bulkEditStartFromText(body)
		stop()
		if err != nil {
			w.err(fmt.Sprintf("%v", err))
			break
		}
		w.clear()
		w.printTabbed(string(original))
		w.Ctl("clean")
		w.github = base
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
		issue, err := writeIssue(old, data, false)
		if err != nil {
			w.err(err.Error())
			return
		}
		if w.mode == modeCreate {
			w.mode = modeSingle
			w.id = getInt(issue.Number)
			w.title = fmt.Sprint(w.id)
			w.Name(w.prefix + w.title)
			all.Lock()
			all.m[w.title] = w
			all.Unlock()
			w.github = issue
		}
		w.load()

	case modeBulk:
		data, err := w.ReadAll("body")
		if err != nil {
			w.err(fmt.Sprintf("Put: %v", err))
			return
		}
		ids, err := bulkWriteIssue(w.github, data, func(s string) { w.err("Put: " + s) })
		if err != nil {
			errText := strings.Replace(err.Error(), "\n", "\t\n", -1)
			if len(ids) > 0 {
				w.err(fmt.Sprintf("updated %d issue%s with errors:\n\t%v", len(ids), suffix(len(ids)), errText))
				break
			}
			w.err(fmt.Sprintf("%s", errText))
			break
		}
		w.err(fmt.Sprintf("updated %d issue%s", len(ids), suffix(len(ids))))

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

func (w *awin) sort() {
	if err := w.Addr("0/^[0-9]/,"); err != nil {
		w.err("nothing to sort")
	}
	data, err := w.ReadAll("xdata")
	if err != nil {
		w.err(err.Error())
		return
	}
	suffix := ""
	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		suffix = "\n"
		lines = lines[:len(lines)-1]
	}
	if w.sortByNumber {
		sort.Stable(byNumber(lines))
	} else {
		sort.Stable(bySecondField(lines))
	}
	w.Addr("0/^[0-9]/,")
	w.Write("data", []byte(strings.Join(lines, "\n")+suffix))
	w.Addr("0")
	w.Ctl("dot=addr")
	w.Ctl("show")
}

type byNumber []string

func (x byNumber) Len() int      { return len(x) }
func (x byNumber) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x byNumber) Less(i, j int) bool {
	return lineNumber(x[i]) > lineNumber(x[j])
}

func lineNumber(s string) int {
	n := 0
	for j := 0; j < len(s) && '0' <= s[j] && s[j] <= '9'; j++ {
		n = n*10 + int(s[j]-'0')
	}
	return n
}

type bySecondField []string

func (x bySecondField) Len() int      { return len(x) }
func (x bySecondField) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x bySecondField) Less(i, j int) bool {
	return skipField(x[i]) < skipField(x[j])
}

func skipField(s string) string {
	i := strings.Index(s, "\t")
	if i < 0 {
		return s
	}
	for i < len(s) && s[i+1] == '\t' {
		i++
	}
	return s[i:]
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
			if cmd == "Sort" {
				if w.mode != modeQuery {
					w.err("can only sort issue list windows")
					break
				}
				w.sortByNumber = !w.sortByNumber
				w.sort()
				break
			}
			if cmd == "Bulk" {
				// TODO(rsc): If Bulk has an argument, treat as search query and use results?
				if w.mode != modeQuery {
					w.err("can only start bulk edit in issue list windows")
					break
				}
				text := w.selection()
				if text == "" {
					data, err := w.ReadAll("body")
					if err != nil {
						w.err(fmt.Sprintf("%v", err))
						break
					}
					text = string(data)
				}
				w.newBulkEdit([]byte(text))
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
			// TODO(rsc): Expand selection, especially for URLs.
			w.loadText(e)
			if !w.look(string(e.Text)) {
				w.WriteEvent(e)
			}
		}
	}
}

func (w *awin) printTabbed(text string) {
	lines := strings.SplitAfter(text, "\n")
	var allRows [][]string
	for _, line := range lines {
		if line == "" {
			continue
		}
		line = strings.TrimSuffix(line, "\n")
		allRows = append(allRows, strings.Split(line, "\t"))
	}

	var buf bytes.Buffer
	for len(allRows) > 0 {
		if row := allRows[0]; len(row) <= 1 {
			if len(row) > 0 {
				buf.WriteString(row[0])
			}
			buf.WriteString("\n")
			allRows = allRows[1:]
			continue
		}

		i := 0
		for i < len(allRows) && len(allRows[i]) > 1 {
			i++
		}

		rows := allRows[:i]
		allRows = allRows[i:]

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
	}

	w.Write("body", buf.Bytes())
}
