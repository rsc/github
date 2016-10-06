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
	RowID   int64 `dbstore:",rowid"`
	Project string
	Type    string
	JSON    []byte `dbstore:",blob"`
}

type History struct {
	RowID   int64 `dbstore:",rowid"`
	Project string
	Issue   int64
	Time    string
	Who     string
	Action  string
	Text    string
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
	storage.Register(new(History))

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

	case "sync":
		var projects []ProjectSync
		if err := storage.Select(db, &projects, ""); err != nil {
			log.Fatalf("reading projects: %v", err)
		}
		for _, proj := range projects {
			doSync(&proj)
		}

	case "refill":
		refill()

	case "dash":
		dash()
	}
}

func doSync(proj *ProjectSync) {
	println("WOULD SYNC", proj.Name)
	syncIssueComments(proj)
	syncIssues(proj)
	syncIssueEvents(proj)
}

func syncIssueComments(proj *ProjectSync) {
	downloadByDate(proj, "/issues/comments", &proj.CommentDate, "CommentDate")
}

func syncIssues(proj *ProjectSync) {
	downloadByDate(proj, "/issues", &proj.IssueDate, "IssueDate")
}

func downloadByDate(proj *ProjectSync, api string, since *string, sinceName string) {
	values := url.Values{
		"client_id":     {auth.ClientID},
		"client_secret": {auth.ClientSecret},
		"sort":          {"updated"},
		"direction":     {"asc"},
		"page":          {"1"},
		"per_page":      {"100"},
	}
	if api == "/issues" {
		values.Set("state", "all")
	}
	if api == "/issues/comments" {
		delete(values, "per_page")
	}
	if *since != "" {
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
				Updated string `json:"updated_at"`
			}
			if err := json.Unmarshal(m, &meta); err != nil {
				return fmt.Errorf("parsing message: %v", err)
			}
			if meta.Updated == "" {
				return fmt.Errorf("parsing message: no updated_at: %s\n", string(m))
			}
			last = meta.Updated

			var raw RawJSON
			raw.Project = proj.Name
			raw.Type = api
			raw.JSON = m
			if err := storage.Insert(tx, &raw); err != nil {
				return fmt.Errorf("writing JSON to database: %v", err)
			}
		}
		*since = last
		if err := storage.Write(tx, proj, sinceName); err != nil {
			return fmt.Errorf("updating database metadata: %v", err)
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

func syncIssueEvents(proj *ProjectSync) {
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
	const api = "/issues/events"
	urlStr := "https://api.github.com/repos/" + proj.Name + api + "?" + values.Encode()
	var (
		firstID   int64
		firstETag string
	)
	done := errors.New("DONE")
	err = downloadPages(urlStr, proj.EventETag, func(resp *http.Response, all []json.RawMessage) error {
		for _, m := range all {
			var meta struct {
				ID int64 `json:"id"`
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
			if proj.EventID != 0 && meta.ID <= proj.EventID {
				return done
			}

			var raw RawJSON
			raw.Project = proj.Name
			raw.Type = api
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
		log.Fatalf("syncing events: %v", err)
	}

	if firstID != 0 {
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
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		println("RESP:", js(resp.Header))

		data, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading body: %v", err)
		}
		if resp.StatusCode != 200 {
			if resp.StatusCode == 500 {
				nfail++
				if nfail < 2 {
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
	Actor struct {
		Login string `json:"login"`
	} `json:"actor"`
	Event string `json:"event"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
	CreatedAt string `json:"created_at"`
	Issue     struct {
		Number int64 `json:"number"`
	} `json:"issue"`
	CommitID string `json:"commit_id"`
	Assignee struct {
		Login string `json:"login"`
	} `json:"assignee"`
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
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Body      string `json:"body"`
}

type ghIssue struct {
	URL  string `json:"url"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Body      string `json:"body"`
	Assignee  struct {
		Login string `json:"login"`
	} `json:"assignee"`
	Milestone struct {
		Title string `json:"title"`
	} `json:"milestone"`
	State       string    `json:"state"`
	PullRequest *struct{} `json:"pull_request"`
}

func refill() {
	if _, err := db.Exec("delete from History"); err != nil {
		log.Fatal(err)
	}
	var last int64
	for {
		var all []RawJSON
		if err := storage.Select(db, &all, "where RowID > ? order by RowID asc limit 100", last); err != nil {
			log.Fatal("sql: %v", err)
		}
		if len(all) == 0 {
			break
		}
		println("GOT", len(all), all[0].RowID, all[len(all)-1].RowID)
		tx, err := db.Begin()
		if err != nil {
			log.Fatal(err)
		}
		for _, m := range all {
			last = m.RowID
			switch m.Type {
			default:
				println("TYPE", m.Type)
			case "/issues/events":
				var ev ghIssueEvent
				if err := json.Unmarshal(m.JSON, &ev); err != nil {
					log.Printf("unmarshal: %v\n%s", err, m.JSON)
					continue
				}
				var h History
				h.RowID = m.RowID * 10
				h.Project = m.Project
				h.Issue = ev.Issue.Number
				h.Time = ev.CreatedAt
				h.Who = ev.Actor.Login
				h.Action = ev.Event
				expectText := true
				switch ev.Event {
				default:
					log.Printf("unknown event: %s\n%s", ev.Event, m.JSON)
					expectText = false
				case "subscribed", "unsubscribed", "reopened", "locked", "unlocked", "head_ref_deleted", "head_ref_restored", "mentioned":
					// ok
					expectText = false
				case "closed", "merged", "referenced":
					h.Text = ev.CommitID
					expectText = ev.Event == "merged"
				case "assigned", "unassigned":
					h.Text = ev.Assignee.Login
				case "labeled", "unlabeled":
					h.Text = ev.Label.Name
				case "milestoned", "demilestoned":
					h.Text = ev.Milestone.Title
				case "renamed":
					if ev.Rename.From != "" {
						h.Text = ev.Rename.From + " → " + ev.Rename.To
					}
				}
				if expectText && h.Text == "" {
					log.Printf("missing text: %s\n%s", ev.Event, m.JSON)
				}
				if err := storage.Insert(tx, &h); err != nil {
					log.Fatal(err)
				}

			case "/issues/comments":
				var ev ghIssueComment
				if err := json.Unmarshal(m.JSON, &ev); err != nil {
					log.Printf("unmarshal: %v\n%s", err, m.JSON)
					continue
				}
				i := strings.LastIndex(ev.IssueURL, "/")
				n, err := strconv.ParseInt(ev.IssueURL[i+1:], 10, 64)
				if err != nil {
					log.Printf("bad issue comment:\n%s", m.JSON)
					continue
				}
				var h History
				h.RowID = m.RowID * 10
				h.Project = m.Project
				h.Issue = n
				h.Time = ev.UpdatedAt
				h.Who = ev.User.Login
				h.Action = "comment"
				h.Text = ev.Body
				if err := storage.Insert(tx, &h); err != nil {
					log.Fatal(err)
				}

			case "/issues":
				var ev ghIssue
				if err := json.Unmarshal(m.JSON, &ev); err != nil {
					log.Printf("unmarshal: %v\n%s", err, m.JSON)
					continue
				}
				i := strings.LastIndex(ev.URL, "/")
				n, err := strconv.ParseInt(ev.URL[i+1:], 10, 64)
				if err != nil {
					log.Printf("bad issue:\n%s", m.JSON)
					continue
				}
				var h History
				h.RowID = m.RowID * 10
				h.Project = m.Project
				h.Issue = n
				h.Time = ev.CreatedAt // best we can do
				h.Who = ev.User.Login
				h.Action = "issue"
				if ev.PullRequest != nil {
					h.Action = "pullrequest"
				}
				h.Text = ev.Body
				if err := storage.Insert(tx, &h); err != nil {
					log.Fatal(err)
				}

				if ev.Assignee.Login != "" {
					h.RowID++
					h.Action = "assign?"
					h.Text = ev.Assignee.Login
					if err := storage.Insert(tx, &h); err != nil {
						log.Fatal(err)
					}
				}
				if ev.Milestone.Title != "" {
					h.RowID++
					h.Action = "milestone?"
					h.Text = ev.Assignee.Login
					if err := storage.Insert(tx, &h); err != nil {
						log.Fatal(err)
					}
				}
				if ev.State != "open" {
					h.RowID++
					h.Action = "close?"
					h.Text = ""
					if err := storage.Insert(tx, &h); err != nil {
						log.Fatal(err)
					}
				}
			}
		}
		if err := tx.Commit(); err != nil {
			log.Fatal(err)
		}
	}
}
