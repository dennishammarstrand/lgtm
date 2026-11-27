# lgtm

A terminal UI for GitHub pull request reviews. Get notified when someone requests your review, read the diff with vim keys, comment on lines, and approve, comment or request changes without leaving the terminal.

Works on macOS, Linux and Windows.

## Install

lgtm uses the [GitHub CLI](https://cli.github.com) for everything, so install it and log in first:

```sh
gh auth login
```

Then install lgtm:

| Platform | Command |
| --- | --- |
| macOS / Linux (Homebrew) | `brew install dennishammarstrand/tap/lgtm` |
| Windows (Scoop) | `scoop bucket add dennishammarstrand https://github.com/dennishammarstrand/scoop-bucket` then `scoop install lgtm` |
| Debian / Ubuntu, Fedora | download the `.deb` or `.rpm` from [Releases](https://github.com/dennishammarstrand/lgtm/releases) |
| Anything with Go | `go install github.com/dennishammarstrand/lgtm@latest` |

### Binary releases

For Windows, macOS or Linux, you can download a binary release [here](https://github.com/dennishammarstrand/lgtm/releases/latest). Unpack the archive and put `lgtm` (`lgtm.exe` on Windows) somewhere on your `PATH`.

## Usage

```sh
lgtm                      # TUI, checks for changes every minute
lgtm -interval 2m         # check less often (GitHub sets the minimum, usually 60s)
lgtm -query "--review-requested=@me --owner=my-org"
lgtm -mine=false          # hide your own PRs
lgtm -theme dracula       # syntax theme (chroma): catppuccin-mocha (default), github-dark, tokyonight-night, …
lgtm check                # poll once, notify, exit (for a scheduler, see below)
lgtm -version
```

Use a terminal with true colour and a font with box-drawing characters (most modern terminals, including Windows Terminal).

## Layout and keys

Two screens of two panes: **PRs | Comments**, then **Files | Diff**. `l` goes PRs → Comments → Files → Diff, `h` goes back. The files column is a tree. Selecting a directory shows the diff of every file under it in one scrollable view; the top row covers the whole PR. `l` goes one level deeper and `h` goes back. `j`/`k` always move within the focused column.

| Key | Action |
| --- | --- |
| `j` / `k` | down / up (moves the line cursor in the diff) |
| `l` / `enter` | go deeper; in the diff with wrap off, pan right |
| `h` / `esc` | go back; in the diff, `h` pans left first, then goes back |
| `gg` / `G`, `ctrl+d` / `ctrl+u` | top / bottom, half page |
| `space` | fold / unfold a directory in the file tree |
| `J` / `K` | next / previous file while in the diff; in the file tree, scroll the diff |
| `n` / `N` | next / previous hunk |
| `s` | toggle side-by-side / unified diff (remembered between runs) |
| `w` | toggle line wrapping (remembered); with wrap off, `h`/`l` pan |
| `enter` | in Comments: reply to the selected thread (inline threads get a threaded reply, the PR conversation a new comment) |
| `V` | in the diff: select lines (stays within one hunk, like GitHub); `esc` cancels |
| `c` / `i` | in the diff: comment on the cursor line or selection. `ctrl+s` adds it to your draft review, `ctrl+o` posts it right away. On a line with a draft it edits the draft |
| `S` | in the diff: suggest a change (pre-filled GitHub suggestion block) |
| `D` | in the diff: delete the draft comment on the cursor line |
| `tab` | in side-by-side: comment on the old or the new side |
| `a` / `c` / `x` | approve / comment / request changes (`ctrl+s` submits). With a selection they act on all selected PRs at once; any that fail stay selected for a retry |
| `space` / `*` | in the PR list: select a PR / every review request with the same title (e.g. one change across many repos); `esc` clears |
| `R` | request reviewers: suggestions, or search people/teams; `tab` selects, `ctrl+s` requests. Re-requests reviewers who already reviewed; `Copilot` requests a Copilot review. With a selection it requests the chosen reviewers on every selected PR (suggestions and search cover all their repos) |
| `o` | open in browser |
| `r` | refresh, `?` help, `q` quit |

`●` marks PRs you haven't opened yet.

The list has two sections: **Review requests** and **My PRs** (your open PRs). Each of your PRs shows its reviewers: ✓ approved, ✗ changes requested, 💬 commented, ⏳ waiting. Only review requests trigger notifications.

The Comments pane shows the selected PR's inline review threads (with a few lines of diff context) and its conversation (comments and review messages, without bot noise). The thread with the most recent comment comes first. A **reply** is a comment by someone else after your first comment in a thread. New replies are marked `NEW`, counted in the PR list and header (`💬 2 new replies`), and trigger a notification, also from `lgtm check`. Opening a PR's comments marks its replies read.

## Inline review

In the diff, `j`/`k` move a line cursor. `V` selects lines, and `c` comments on them. Comments go into a **draft review**: drafts are kept locally (they survive a restart), show as `✎` notes under their lines and as `✎ N drafts` in the PR list. `a`/`x` (or `c` from the other panes) submit the review with all drafts in one go. Existing review threads show as `💬` notes under their lines; `enter` opens the thread in the Comments pane to read and reply.

Diffs are syntax highlighted, and within a changed line the exact words that changed get a stronger background.

GitHub removes the review request as soon as you submit any review, including a comment. lgtm keeps such PRs in the list, marked `💬 commented` or `✗ changes requested`, until you approve them or they are merged or closed. If the author removes you as a reviewer before you've reviewed, the PR is dropped.

## Notifications

lgtm watches your GitHub notifications with a conditional request (`If-None-Match`). An unchanged answer (`304`) is free: it doesn't count against your rate limit. lgtm only runs the full PR search when a notification arrives. It also does a full refresh every 10 minutes, because a removed review request or a merged PR doesn't always create a notification. GitHub's `X-Poll-Interval` (normally 60s) sets the minimum interval.

Desktop notifications use Notification Center on macOS (install `terminal-notifier` to open the PR by clicking the notification), the desktop notification service on Linux, and toasts on Windows.

The TUI notifies while it's running. To also get notified when it's closed, run `lgtm check` on a schedule. Both share the same state, so each request is only notified once. The very first run records the existing requests without notifying. State lives in `$XDG_STATE_HOME/lgtm` (default `~/.local/state/lgtm`), or `%LOCALAPPDATA%\lgtm` on Windows.

### macOS (launchd)

`~/Library/LaunchAgents/lgtm.check.plist` (adjust the path to `lgtm`, see `which lgtm`):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>lgtm.check</string>
  <key>ProgramArguments</key>
  <array><string>/opt/homebrew/bin/lgtm</string><string>check</string></array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
  <key>StartInterval</key><integer>120</integer>
  <key>StandardErrorPath</key><string>/tmp/lgtm.err</string>
</dict>
</plist>
```

```sh
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/lgtm.check.plist
```

### Linux (systemd)

`~/.config/systemd/user/lgtm-check.service`:

```ini
[Service]
Type=oneshot
ExecStart=%h/.local/bin/lgtm check
```

`~/.config/systemd/user/lgtm-check.timer`:

```ini
[Timer]
OnBootSec=1min
OnUnitActiveSec=2min

[Install]
WantedBy=timers.target
```

```sh
systemctl --user enable --now lgtm-check.timer
```

Adjust `ExecStart` to where `lgtm` is installed (`which lgtm`).

### Windows (Task Scheduler)

```powershell
schtasks /create /tn "lgtm check" /sc minute /mo 2 /tr "lgtm check"
```

A console window flashes briefly each time it runs.

## Building from source

```sh
make install              # builds into ~/.local/bin (override with BIN_DIR=...)
make test
```

Releases are built by [GoReleaser](https://goreleaser.com) when a `v*` tag is pushed.
