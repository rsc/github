// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"rsc.io/dbstore"
	_ "rsc.io/sqlite"
)

// TODO: pragma journal_mode=WAL

// Database tables. DO NOT CHANGE.

type Auth struct {
	Key          string `dbstore:",key"`
	ClientID     string
	ClientSecret string
}

type ProjectSync struct {
	Name        string `dbstore:",key"` // "owner/repo"
	EventETag   string
	EventID     int64
	IssueDate   string
	CommentDate string
	RefillID    int64
}

type RawJSON struct {
	URL     string `dbstore:",key"`
	Project string
	Issue   int64
	Type    string
	JSON    []byte `dbstore:",blob"`
	Time    string
}

var (
	file    = flag.String("f", os.Getenv("HOME")+"/githubissue.db", "database `file` to use")
	storage = new(dbstore.Storage)
	db      *sql.DB
	auth    Auth
)

func usage() {
	fmt.Fprintf(os.Stderr, `usage: issuedb [-f db] command [args]

Commands are:

	init <clientid> <clientsecret> (initialize new database)
	add <owner/repo> (add new repository)
	sync (sync repositories)
	resync (full resync to catch very old events)

The default database is $HOME/githubissue.db.
`)
	os.Exit(2)
}

func main() {
	log.SetPrefix("issuedb: ")
	log.SetFlags(0)

	storage.Register(new(Auth))
	storage.Register(new(ProjectSync))
	storage.Register(new(RawJSON))

	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}

	if args[0] == "init" {
		if len(args) != 3 {
			fmt.Fprintf(os.Stderr, "usage: issuedb [-f db] init clientid clientsecret\n")
			os.Exit(2)
		}
		_, err := os.Stat(*file)
		if err == nil {
			log.Fatalf("creating database: file %s already exists", *file)
		}
		db, err := sql.Open("sqlite3", *file)
		if err != nil {
			log.Fatalf("creating database: %v", err)
		}
		defer db.Close()
		if err := storage.CreateTables(db); err != nil {
			log.Fatalf("initializing database: %v", err)
		}
		auth = Auth{Key: "unauth", ClientID: args[1], ClientSecret: args[2]}
		if err := storage.Insert(db, &auth); err != nil {
			log.Fatal(err)
		}
		return
	}

	_, err := os.Stat(*file)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	db, err = sql.Open("sqlite3", *file)
	if err != nil {
		log.Fatalf("opening database: %v", err)
	}
	defer db.Close()

	auth.Key = "unauth"
	if err := storage.Read(db, &auth, "ALL"); err != nil {
		log.Fatalf("reading database: %v", err)
	}

	// TODO: Remove or deal with better.
	// This is here so that if we add new tables they get created in old databases.
	// But there is nothing to recreate or expand tables in old databases.

	switch args[0] {
	default:
		usage()

	case "add":
		if len(args) != 2 {
			fmt.Fprintf(os.Stderr, "usage: issuedb [-f db] add owner/repo\n")
			os.Exit(2)
		}
		var proj ProjectSync
		proj.Name = args[1]
		if err := storage.Read(db, &proj); err == nil {
			log.Fatalf("project %s already stored in database", proj.Name)
		}

		proj.Name = args[1]
		if err := storage.Insert(db, &proj); err != nil {
			log.Fatalf("adding project: %v", err)
		}
		return

	case "sync", "resync":
		var projects []ProjectSync
		if err := storage.Select(db, &projects, ""); err != nil {
			log.Fatalf("reading projects: %v", err)
		}
		for _, proj := range projects {
			if match(proj.Name, args[1:]) {
				doSync(&proj, args[0] == "resync")
			}
		}
		for _, arg := range args[1:] {
			if arg != didArg {
				log.Printf("unknown project: %s", arg)
			}
		}

	case "retime":
		retime()

	case "todo":
		var projects []ProjectSync
		if err := storage.Select(db, &projects, ""); err != nil {
			log.Fatalf("reading projects: %v", err)
		}
		for _, proj := range projects {
			if match(proj.Name, args[1:]) {
				todo(&proj)
			}
		}
		for _, arg := range args[1:] {
			if arg != didArg {
				log.Printf("unknown project: %s", arg)
			}
		}
	}
}

const didArg = "\x00"

func match(name string, args []string) bool {
	if len(args) == 0 {
		return true
	}
	ok := false
	for i, arg := range args {
		if name == arg {
			args[i] = didArg
			ok = true
		}
	}
	return ok
}

func doSync(proj *ProjectSync, resync bool) {
	println("WOULD SYNC", proj.Name)
	syncIssues(proj)
	syncIssueComments(proj)
	if resync {
		syncIssueEvents(proj, 0, true)
		syncIssueEventsByIssue(proj)
	} else {
		syncIssueEvents(proj, 0, false)
	}
}

func syncIssueComments(proj *ProjectSync) {
	downloadByDate(proj, "/issues/comments", &proj.CommentDate, "CommentDate")
}

func syncIssues(proj *ProjectSync) {
	downloadByDate(proj, "/issues", &proj.IssueDate, "IssueDate")
}

func downloadByDate(proj *ProjectSync, api string, since *string, sinceName string) {
	values := url.Values{
		"sort":      {"updated"},
		"direction": {"asc"},
		"page":      {"1"},
		"per_page":  {"100"},
	}
	if api == "/issues" {
		values.Set("state", "all")
	}
	if api == "/issues/comments" {
		delete(values, "per_page")
	}
	if since != nil && *since != "" {
		values.Set("since", *since)
	}
	urlStr := "https://api.github.com/repos/" + proj.Name + api + "?" + values.Encode()

	err := downloadPages(urlStr, "", func(_ *http.Response, all []json.RawMessage) error {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("starting db transaction: %v", err)
		}
		defer tx.Rollback()
		var last string
		for _, m := range all {
			var meta struct {
				URL       string
				Updated   string `json:"updated_at"`
				Number    int64  // for /issues feed
				IssueURL  string `json:"issue_url"` // for /issues/comments feed
				CreatedAt string `json:"created_at"`
			}
			if err := json.Unmarshal(m, &meta); err != nil {
				return fmt.Errorf("parsing message: %v", err)
			}
			if meta.Updated == "" {
				return fmt.Errorf("parsing message: no updated_at: %s", string(m))
			}
			last = meta.Updated

			var raw RawJSON
			raw.URL = meta.URL
			raw.Project = proj.Name
			switch api {
			default:
				log.Fatalf("downloadByDate: unknown API: %v", api)
			case "/issues":
				raw.Issue = meta.Number
			case "/issues/comments":
				i := strings.LastIndex(meta.IssueURL, "/")
				n, err := strconv.ParseInt(meta.IssueURL[i+1:], 10, 64)
				if err != nil {
					log.Fatalf("cannot find issue number in /issues/comments API: %v", urlStr)
				}
				raw.Issue = n
			}
			raw.Type = api
			raw.JSON = m
			raw.Time = meta.CreatedAt
			if err := storage.Insert(tx, &raw); err != nil {
				return fmt.Errorf("writing JSON to database: %v", err)
			}
		}
		if since != nil {
			*since = last
			if err := storage.Write(tx, proj, sinceName); err != nil {
				return fmt.Errorf("updating database metadata: %v", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		log.Fatal(err)
	}
}

func syncIssueEvents(proj *ProjectSync, id int, short bool) {
	tx, err := db.Begin()
	if err != nil {
		log.Fatalf("starting db transaction: %v", err)
	}
	defer tx.Rollback()

	values := url.Values{
		"client_id":     {auth.ClientID},
		"client_secret": {auth.ClientSecret},
		"page":          {"1"},
		"per_page":      {"100"},
	}
	var api = "/issues/events"
	if id > 0 {
		api = fmt.Sprintf("/issues/%d/events", id)
	}
	urlStr := "https://api.github.com/repos/" + proj.Name + api + "?" + values.Encode()
	var (
		firstID   int64
		firstETag string
	)
	done := errors.New("DONE")
	err = downloadPages(urlStr, proj.EventETag, func(resp *http.Response, all []json.RawMessage) error {
		for _, m := range all {
			var meta struct {
				ID    int64  `json:"id"`
				URL   string `json:"url"`
				Issue struct {
					Number int64
				}
			}
			if err := json.Unmarshal(m, &meta); err != nil {
				return fmt.Errorf("parsing message: %v", err)
			}
			if meta.ID == 0 {
				return fmt.Errorf("parsing message: no id: %s", string(m))
			}
			println(meta.ID)
			if firstID == 0 {
				firstID = meta.ID
				firstETag = resp.Header.Get("Etag")
			}
			if id == 0 && (proj.EventID != 0 && meta.ID <= proj.EventID || short) {
				return done
			}

			var raw RawJSON
			raw.URL = meta.URL
			raw.Project = proj.Name
			raw.Type = "/issues/events"
			if id > 0 {
				raw.Issue = int64(id)
			} else {
				raw.Issue = meta.Issue.Number
			}
			raw.JSON = m
			if err := storage.Insert(tx, &raw); err != nil {
				return fmt.Errorf("writing JSON to database: %v", err)
			}
		}
		return nil
	})
	if err == done {
		err = nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "304 Not Modified") {
			return
		}
		log.Fatalf("syncing events: %v", err)
	}

	if id == 0 && firstID != 0 {
		proj.EventID = firstID
		proj.EventETag = firstETag
		if err := storage.Write(tx, proj, "EventID", "EventETag"); err != nil {
			log.Fatalf("updating database metadata: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}

func syncIssueEventsByIssue(proj *ProjectSync) {
	rows, err := db.Query("select URL from RawJSON where Type = ? group by URL", "/issues")
	if err != nil {
		log.Fatal(err)
	}
	var ids []int
	suffix := "repos/" + proj.Name + "/issues/"
	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			log.Fatal(err)
		}
		i := strings.LastIndex(url, "/")
		if !strings.HasSuffix(url[:i+1], suffix) {
			continue
		}
		id, err := strconv.Atoi(url[i+1:])
		if err != nil {
			log.Fatal(url, err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		println("ID", id)
		syncIssueEvents(proj, id, false)
	}
}

func downloadPages(url, etag string, do func(*http.Response, []json.RawMessage) error) error {
	nfail := 0
	for n := 0; url != ""; n++ {
	again:
		println("URL:", url)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		req.SetBasicAuth(auth.ClientID, auth.ClientSecret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		//println("RESP:", js(resp.Header))

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading body: %v", err)
		}
		if resp.StatusCode != 200 {
			if resp.StatusCode == 403 {
				if resp.Header.Get("X-Ratelimit-Remaining") == "0" {
					n, _ := strconv.Atoi(resp.Header.Get("X-Ratelimit-Reset"))
					if n > 0 {
						t := time.Unix(int64(n), 0)
						println("RATELIMIT", t.String())
						time.Sleep(t.Sub(time.Now()) + 1*time.Minute)
						goto again
					}
				}
			}
			if resp.StatusCode == 500 || resp.StatusCode == 502 {
				nfail++
				if nfail < 2 {
					println("REPEAT:", resp.Status, string(data))
					time.Sleep(time.Duration(nfail) * 2 * time.Second)
					goto again
				}
			}
			return fmt.Errorf("%s\n%s", resp.Status, data)
		}
		checkRateLimit(resp)

		var all []json.RawMessage
		if err := json.Unmarshal(data, &all); err != nil {
			return fmt.Errorf("parsing body: %v", err)
		}
		println("GOT", len(all), "messages")

		if err := do(resp, all); err != nil {
			return err
		}

		url = findNext(resp.Header.Get("Link"))
	}
	return nil
}

func findNext(link string) string {
	for link != "" {
		link = strings.TrimSpace(link)
		if !strings.HasPrefix(link, "<") {
			break
		}
		i := strings.Index(link, ">")
		if i < 0 {
			break
		}
		linkURL := link[1:i]
		link = strings.TrimSpace(link[i+1:])
		for strings.HasPrefix(link, ";") {
			link = strings.TrimSpace(link[1:])
			i := strings.Index(link, ";")
			j := strings.Index(link, ",")
			if i < 0 || j >= 0 && j < i {
				i = j
			}
			if i < 0 {
				i = len(link)
			}
			attr := strings.TrimSpace(link[:i])
			if attr == `rel="next"` {
				return linkURL
			}
			link = link[i:]
		}
		if !strings.HasPrefix(link, ",") {
			break
		}
		link = strings.TrimSpace(link[1:])
	}
	return ""
}

func checkRateLimit(resp *http.Response) {
	// TODO
}

func js(x interface{}) string {
	data, err := json.MarshalIndent(x, "", "\t")
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return string(data)
}

type ghIssueEvent struct {
	// NOTE: Issue field is not present when downloading for a specific issue,
	// only in the master feed for the whole repo. So do not add it here.
	Actor struct {
		Login string `json:"login"`
	} `json:"actor"`
	Event  string `json:"event"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	LockReason string `json:"lock_reason"`
	CreatedAt  string `json:"created_at"`
	CommitID   string `json:"commit_id"`
	Assigner   struct {
		Login string `json:"login"`
	} `json:"assigner"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone struct {
		Title string `json:"title"`
	} `json:"milestone"`
	Rename struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rename"`
}

type ghIssueComment struct {
	IssueURL string `json:"issue_url"`
	HTMLURL  string `json:"html_url"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Body      string `json:"body"`
}

type ghIssue struct {
	URL     string `json:"url"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	ClosedAt  string `json:"closed_at"`
	Body      string `json:"body"`
	Assignees []struct {
		Login string `json:"login"`
	} `json:"assignees"`
	Milestone struct {
		Title string `json:"title"`
	} `json:"milestone"`
	State            string    `json:"state"`
	PullRequest      *struct{} `json:"pull_request"`
	Locked           bool
	ActiveLockReason string `json:"active_lock_reason"`
	Labels           []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func retime() {
	last := ""
	for {
		var all []RawJSON
		if err := storage.Select(db, &all, "where URL > ? and Time = ? order by URL asc limit 100", last, ""); err != nil {
			log.Fatalf("sql: %v", err)
		}
		if len(all) == 0 {
			break
		}
		println("GOT", len(all), all[0].URL, all[0].Type, all[len(all)-1].URL, all[len(all)-1].Type)
		tx, err := db.Begin()
		if err != nil {
			log.Fatal(err)
		}
		for _, m := range all {
			var meta struct {
				CreatedAt string `json:"created_at"`
			}
			if err := json.Unmarshal(m.JSON, &meta); err != nil {
				log.Fatal(err)
			}
			if meta.CreatedAt == "" {
				log.Fatalf("missing created_at: %s", m.JSON)
			}
			tm, err := time.Parse(time.RFC3339, meta.CreatedAt)
			if err != nil {
				log.Fatalf("parse: %v", err)
			}
			if _, err := tx.Exec("update RawJSON set Time = ? where URL = ?", tm.UTC().Format(time.RFC3339Nano), m.URL); err != nil {
				log.Fatal(err)
			}
			last = m.URL
		}
		if err := tx.Commit(); err != nil {
			log.Fatal(err)
		}
	}
}
