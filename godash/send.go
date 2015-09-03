// Copyright 2015 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build ignore

package main

import (
	"flag"
	"io/ioutil"
	"log"
	"net/smtp"
	"os"
)

var post = flag.Bool("post", false, "post to golang-dev")

func main() {
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
	}

	msg, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		log.Fatal(err)
	}
	if len(msg) == 0 {
		log.Fatal("no message")
	}
	if *post {
		err = smtp.SendMail("alt1.gmr-smtp-in.l.google.com:smtp", nil, "rsc@golang.org", []string{"golang-dev@googlegroups.com"}, msg)
	} else {
		err = smtp.SendMail("aspmx.l.google.com:smtp", nil, "rsc@golang.org", []string{"rsc@google.com"}, msg)
	}
	if err != nil {
		log.Fatal(err)
	}
}
