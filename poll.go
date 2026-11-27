package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// fullRefreshEvery bounds how long lgtm trusts "no new notifications": some
// changes (a review request removed, a watched PR merged) don't create one.
const fullRefreshEvery = 10 * time.Minute

type notifResult struct {
	changed bool
	etag    string
	poll    time.Duration // GitHub's requested minimum poll interval
}

// checkNotifications asks GitHub whether your notifications changed since etag.
// An unchanged answer (304) doesn't count against the API rate limit, so this is
// cheap to call every minute; lgtm only does its full fetch when it says changed.
func checkNotifications(ctx context.Context, etag string) (notifResult, error) {
	args := []string{"api", "--include", "notifications?per_page=1"}
	if etag != "" {
		args = append(args, "-H", "If-None-Match: "+etag)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run() // gh exits non-zero on 304, so judge by the status line

	res := notifResult{etag: etag}
	sc := bufio.NewScanner(&stdout)
	status := 0
	if sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 {
			status, _ = strconv.Atoi(f[1])
		}
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			break
		}
		name, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch strings.ToLower(name) {
		case "etag":
			res.etag = value
		case "x-poll-interval":
			if s, err := strconv.Atoi(value); err == nil {
				res.poll = time.Duration(s) * time.Second
			}
		}
	}
	switch status {
	case 304:
		return res, nil
	case 200:
		res.changed = true
		return res, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	return res, fmt.Errorf("gh api notifications: %s", msg)
}
