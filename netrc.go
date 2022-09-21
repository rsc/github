// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package github

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func netrcAuth(host, user string) (string, string, error) {
	netrc := ".netrc"
	if runtime.GOOS == "windows" {
		netrc = "_netrc"
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	data, _ := ioutil.ReadFile(filepath.Join(homeDir, netrc))
	for _, line := range strings.Split(string(data), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) >= 6 && f[0] == "machine" && f[1] == host && f[2] == "login" && f[4] == "password" && (user == "" || f[3] == user) {
			return f[3], f[5], nil
		}
	}
	return "", "", fmt.Errorf("cannot find netrc entry for %s", host)
}
