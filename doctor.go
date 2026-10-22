package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GitJournal/git-auto-sync/common"
	cfg "github.com/GitJournal/git-auto-sync/common/config"
	"github.com/kirsle/configdir"
	cli "github.com/urfave/cli/v2"
)

const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"

	launchAgentName = "git-auto-sync-daemon"
)

type DoctorCheck struct {
	Section string `json:"section"`
	Repo    string `json:"repo,omitempty"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

type DoctorSummary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

type DoctorReport struct {
	OK      bool          `json:"ok"`
	Summary DoctorSummary `json:"summary"`
	Checks  []DoctorCheck `json:"checks"`
}

type doctorEnv struct {
	home          string
	goos          string
	configPath    string
	socketPath    string
	logPath       string
	stateDir      string
	cliVersion    string
	executable    string
	checkXcode    bool
	cmdTimeout    time.Duration
	remoteTimeout time.Duration
}

func newDoctorEnv() doctorEnv {
	home, _ := os.UserHomeDir()
	exe, _ := os.Executable()
	return doctorEnv{
		home:          home,
		goos:          runtime.GOOS,
		configPath:    filepath.Join(configdir.LocalConfig("git-auto-sync"), "config.json"),
		socketPath:    common.DefaultSocketPath(),
		logPath:       common.LogPath(),
		stateDir:      common.StateDir(),
		cliVersion:    version,
		executable:    exe,
		checkXcode:    runtime.GOOS == "darwin",
		cmdTimeout:    10 * time.Second,
		remoteTimeout: 15 * time.Second,
	}
}

type doctorRun struct {
	env    doctorEnv
	checks []DoctorCheck
	envs   []string
}

func (r *doctorRun) add(c DoctorCheck) {
	r.checks = append(r.checks, c)
}

func doctor(ctx *cli.Context) error {
	report := runDoctor(newDoctorEnv())
	if ctx.Bool("json") {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		printDoctorReport(os.Stdout, report)
	}
	if !report.OK {
		return cli.Exit("", 1)
	}
	return nil
}

func runDoctor(env doctorEnv) DoctorReport {
	r := &doctorRun{env: env}

	r.checkGit()
	repos := r.checkConfig()
	usesGitHub, httpsGitHub := r.checkRepos(repos)
	if usesGitHub {
		r.checkGH(httpsGitHub)
	}
	r.checkDaemon(len(repos) > 0)
	r.checkLaunchAgent()
	r.checkLogs()

	rank := map[string]int{"system": 0, "config": 1, "repo": 2, "daemon": 3, "logs": 4}
	sort.SliceStable(r.checks, func(i, j int) bool { return rank[r.checks[i].Section] < rank[r.checks[j].Section] })

	report := DoctorReport{OK: true, Checks: r.checks}
	for _, c := range r.checks {
		switch c.Status {
		case checkOK:
			report.Summary.OK++
		case checkWarn:
			report.Summary.Warn++
		case checkFail:
			report.Summary.Fail++
			report.OK = false
		}
	}
	return report
}

func printDoctorReport(out io.Writer, report DoctorReport) {
	symbols := map[string]string{checkOK: "✓", checkWarn: "⚠", checkFail: "✗"}
	section, repo := "", ""
	for _, c := range report.Checks {
		if c.Section != section || c.Repo != repo {
			if section != "" {
				fmt.Fprintln(out)
			}
			section, repo = c.Section, c.Repo
			title := strings.ToUpper(section[:1]) + section[1:]
			if repo != "" {
				title = repo
			}
			fmt.Fprintln(out, title)
		}
		line := fmt.Sprintf("  %s %s", symbols[c.Status], c.Name)
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		fmt.Fprintln(out, line)
		if c.Fix != "" && c.Status != checkOK {
			fmt.Fprintln(out, "      fix: "+c.Fix)
		}
	}
	fmt.Fprintf(out, "\n%d ok, %d warning(s), %d problem(s)\n", report.Summary.OK, report.Summary.Warn, report.Summary.Fail)
}

type cmdResult struct {
	stdout   string
	stderr   string
	exitCode int
	timedOut bool
	err      error
}

func (c cmdResult) output() string {
	return strings.TrimSpace(c.stdout + "\n" + c.stderr)
}

func (c cmdResult) gitError(args []string) error {
	if c.err == nil {
		return nil
	}
	return &common.GitError{Args: args, Stdout: c.stdout, Stderr: c.stderr, ExitCode: c.exitCode, TimedOut: c.timedOut, Err: c.err}
}

func runCommand(timeout time.Duration, dir string, env []string, name string, args ...string) cmdResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()

	res := cmdResult{stdout: stdout.String(), stderr: stderr.String(), err: err, exitCode: -1}
	if err == nil {
		res.exitCode = 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.exitCode = exitErr.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.timedOut = true
	}
	return res
}

func doctorPath() string {
	p := os.Getenv("PATH")
	if runtime.GOOS == "windows" {
		return p
	}
	parts := filepath.SplitList(p)
	for _, extra := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		found := false
		for _, part := range parts {
			if filepath.Clean(part) == extra {
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, extra)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

func (r *doctorRun) commandEnv() []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "PATH="+doctorPath())
	env = append(env, r.envs...)
	env = append(env, "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	}
	return env
}

func (r *doctorRun) git(timeout time.Duration, dir string, args ...string) cmdResult {
	return runCommand(timeout, dir, r.commandEnv(), "git", args...)
}

func lookPathIn(name, path string) (string, error) {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func (r *doctorRun) checkGit() {
	const section = "system"
	gitPath, err := exec.LookPath("git")
	if err != nil {
		gitPath, err = lookPathIn("git", doctorPath())
	}
	if err != nil {
		fix := "install git and make sure it is on PATH"
		if r.env.goos == "darwin" {
			fix = "xcode-select --install"
		}
		r.add(DoctorCheck{Section: section, Name: "git", Status: checkFail, Detail: "git was not found on PATH", Fix: fix})
		return
	}

	res := runCommand(r.env.cmdTimeout, "", r.commandEnv(), gitPath, "--version")
	switch {
	case res.err == nil:
		r.add(DoctorCheck{Section: section, Name: "git", Status: checkOK, Detail: firstLine(res.stdout) + " (" + gitPath + ")"})
	case common.ClassifyError(res.gitError([]string{"--version"})) == common.CodeXcodeLicense:
		r.add(DoctorCheck{Section: section, Name: "git", Status: checkFail, Detail: "the Xcode license has not been accepted, so git refuses to run", Fix: "sudo xcodebuild -license accept"})
		return
	default:
		r.add(DoctorCheck{Section: section, Name: "git", Status: checkFail, Detail: truncate(firstLine(res.output()), 120), Fix: "reinstall git, or run `xcode-select --install` on macOS"})
		return
	}

	if !r.env.checkXcode {
		return
	}
	sel := runCommand(r.env.cmdTimeout, "", r.commandEnv(), "/usr/bin/xcode-select", "-p")
	if sel.err != nil {
		r.add(DoctorCheck{Section: section, Name: "Xcode license", Status: checkWarn, Detail: "the command line developer tools are not installed, so /usr/bin/git (used under launchd) will not work", Fix: "xcode-select --install"})
		return
	}
	if gitPath == "/usr/bin/git" {
		r.add(DoctorCheck{Section: section, Name: "Xcode license", Status: checkOK, Detail: "accepted"})
		return
	}
	sys := runCommand(r.env.cmdTimeout, "", r.commandEnv(), "/usr/bin/git", "--version")
	switch {
	case sys.err == nil:
		r.add(DoctorCheck{Section: section, Name: "Xcode license", Status: checkOK, Detail: "accepted"})
	case common.ClassifyError(sys.gitError([]string{"--version"})) == common.CodeXcodeLicense:
		r.add(DoctorCheck{Section: section, Name: "Xcode license", Status: checkFail, Detail: "not accepted: /usr/bin/git refuses to run, and the daemon may use it under launchd", Fix: "sudo xcodebuild -license accept"})
	default:
		r.add(DoctorCheck{Section: section, Name: "Xcode license", Status: checkWarn, Detail: "/usr/bin/git failed: " + truncate(firstLine(sys.output()), 100)})
	}
}

func (r *doctorRun) checkConfig() []string {
	const section = "config"
	data, err := os.ReadFile(r.env.configPath)
	if errors.Is(err, os.ErrNotExist) {
		r.add(DoctorCheck{Section: section, Name: "config", Status: checkWarn, Detail: "no config file yet, so no repos are synced", Fix: "git-auto-sync daemon add <repo>"})
		return nil
	}
	if err != nil {
		r.add(DoctorCheck{Section: section, Name: "config", Status: checkFail, Detail: err.Error()})
		return nil
	}
	var config cfg.Config
	if len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			r.add(DoctorCheck{Section: section, Name: "config", Status: checkFail, Detail: "cannot parse " + r.env.configPath + ": " + err.Error(), Fix: "fix the JSON by hand, or move the file away and add your repos again"})
			return nil
		}
	}
	r.envs = append([]string(nil), config.Envs...)
	repos := cfg.NormalizeRepos(config.Repos)
	if len(repos) == 0 {
		r.add(DoctorCheck{Section: section, Name: "config", Status: checkWarn, Detail: "no repos configured (" + r.env.configPath + ")", Fix: "git-auto-sync daemon add <repo>"})
		return nil
	}
	r.add(DoctorCheck{Section: section, Name: "config", Status: checkOK, Detail: fmt.Sprintf("%d repo(s) in %s", len(repos), r.env.configPath)})
	return repos
}

type repoFacts struct {
	checks      []DoctorCheck
	github      bool
	httpsGitHub bool
}

func (r *doctorRun) checkRepos(repos []string) (bool, bool) {
	results := make([]repoFacts, len(repos))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repo string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = r.checkRepo(repo)
		}(i, repo)
	}
	wg.Wait()

	usesGitHub, httpsGitHub := false, false
	for _, res := range results {
		r.checks = append(r.checks, res.checks...)
		usesGitHub = usesGitHub || res.github
		httpsGitHub = httpsGitHub || res.httpsGitHub
	}
	return usesGitHub, httpsGitHub
}

func (r *doctorRun) checkRepo(repo string) repoFacts {
	var f repoFacts
	add := func(name, status, detail, fix string) {
		f.checks = append(f.checks, DoctorCheck{Section: "repo", Repo: repo, Name: name, Status: status, Detail: detail, Fix: fix})
	}
	rm := "git-auto-sync daemon rm " + shellQuote(repo)

	info, err := os.Stat(repo)
	if err != nil {
		add("folder", checkFail, "the folder is missing", "restore it, or stop syncing it: "+rm)
		return f
	}
	if !info.IsDir() {
		add("folder", checkFail, "not a folder", rm)
		return f
	}

	top := r.git(r.env.cmdTimeout, repo, "rev-parse", "--show-toplevel")
	if top.err != nil {
		code := common.ClassifyError(top.gitError([]string{"rev-parse"}))
		if code == common.CodeXcodeLicense {
			add("repository", checkFail, "git refuses to run until the Xcode license is accepted", "sudo xcodebuild -license accept")
		} else {
			add("repository", checkFail, "not a git repository", rm)
		}
		return f
	}
	root := strings.TrimSpace(top.stdout)
	if common.ResolvePath(root) != common.ResolvePath(repo) {
		add("repository", checkWarn, "this folder is inside the repo at "+root, rm+" && git-auto-sync daemon add "+shellQuote(root))
	} else {
		add("repository", checkOK, "", "")
	}

	gitC := "git -C " + shellQuote(repo)

	name := strings.TrimSpace(r.git(r.env.cmdTimeout, repo, "config", "user.name").stdout)
	email := strings.TrimSpace(r.git(r.env.cmdTimeout, repo, "config", "user.email").stdout)
	switch {
	case email == "":
		add("author", checkFail, "user.email is not set", `git config --global user.email "you@example.com"`)
	case name == "":
		add("author", checkFail, "user.name is not set", `git config --global user.name "Your Name"`)
	default:
		add("author", checkOK, name+" <"+email+">", "")
	}

	if dir := r.git(r.env.cmdTimeout, repo, "rev-parse", "--git-path", "rebase-merge", "--git-path", "rebase-apply", "--git-path", "MERGE_HEAD"); dir.err == nil {
		busy := ""
		for i, p := range strings.Split(strings.TrimSpace(dir.stdout), "\n") {
			if !filepath.IsAbs(p) {
				p = filepath.Join(repo, p)
			}
			if _, err := os.Stat(p); err == nil {
				if i == 2 {
					busy = "merge"
				} else {
					busy = "rebase"
				}
				break
			}
		}
		if busy != "" {
			add("state", checkFail, "a "+busy+" is in progress, so the daemon will not sync", "finish or abort it: "+gitC+" "+busy+" --abort")
			return f
		}
	}

	branchRes := r.git(r.env.cmdTimeout, repo, "symbolic-ref", "--short", "-q", "HEAD")
	branch := strings.TrimSpace(branchRes.stdout)
	if branchRes.err != nil || branch == "" {
		add("branch", checkFail, "HEAD is detached, so there is no branch to sync", gitC+" switch <branch>")
		return f
	}

	remote := strings.TrimSpace(r.git(r.env.cmdTimeout, repo, "config", "branch."+branch+".remote").stdout)
	merge := strings.TrimSpace(r.git(r.env.cmdTimeout, repo, "config", "branch."+branch+".merge").stdout)
	up := r.git(r.env.cmdTimeout, repo, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if up.err != nil || remote == "" || merge == "" {
		suggest := "origin"
		if remotes := strings.Fields(r.git(r.env.cmdTimeout, repo, "remote").stdout); len(remotes) > 0 && !contains(remotes, "origin") {
			suggest = remotes[0]
		}
		add("upstream", checkFail, "branch '"+branch+"' has no upstream", gitC+" branch -u "+suggest+"/"+branch)
		return f
	}
	add("upstream", checkOK, branch+" → "+strings.TrimSpace(up.stdout), "")

	if remote == "." {
		add("remote", checkOK, "upstream is a local branch", "")
		return f
	}

	url := strings.TrimSpace(r.git(r.env.cmdTimeout, repo, "remote", "get-url", remote).stdout)
	if strings.Contains(url, "github.com") {
		f.github = true
		if strings.HasPrefix(url, "https://") {
			f.httpsGitHub = true
		}
	}

	args := []string{}
	if f.github {
		args = append(args, "-c", "credential.https://github.com.helper=!gh auth git-credential")
	}
	args = append(args, "ls-remote", "--exit-code", remote, merge)
	ls := r.git(r.env.remoteTimeout, repo, args...)
	switch {
	case ls.err == nil:
		add("remote", checkOK, remote+" ("+redactURL(url)+") is reachable", "")
	case ls.timedOut:
		add("remote", checkWarn, fmt.Sprintf("%s did not answer within %s (offline?)", remote, r.env.remoteTimeout), "")
	case ls.exitCode == 2:
		add("remote", checkFail, strings.TrimPrefix(merge, "refs/heads/")+" no longer exists on "+remote, gitC+" branch -u "+remote+"/<branch>")
	default:
		detail := truncate(gitErrorLine(ls.stderr), 140)
		switch common.ClassifyError(ls.gitError([]string{"ls-remote"})) {
		case common.CodeNetwork, common.CodeTimeout:
			add("remote", checkWarn, remote+" is unreachable (offline?): "+detail, "")
		case common.CodeAuth:
			fix := "check your credentials for " + redactURL(url)
			switch {
			case f.httpsGitHub:
				fix = "gh auth login"
			case strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://"):
				fix = "make sure your SSH key is loaded (ssh-add) and the daemon has SSH_AUTH_SOCK: git-auto-sync daemon env SSH_AUTH_SOCK=$SSH_AUTH_SOCK"
			}
			add("remote", checkFail, "authentication failed for "+remote+": "+detail, fix)
		default:
			add("remote", checkFail, remote+": "+detail, "")
		}
	}
	return f
}

func (r *doctorRun) checkGH(httpsGitHub bool) {
	const section = "system"
	missing := checkWarn
	if httpsGitHub {
		missing = checkFail
	}
	ghPath, err := lookPathIn("gh", doctorPath())
	if err != nil {
		r.add(DoctorCheck{Section: section, Name: "gh", Status: missing, Detail: "a repo uses GitHub but the gh CLI was not found (the daemon uses it for HTTPS credentials)", Fix: "brew install gh && gh auth login"})
		return
	}
	res := runCommand(r.env.remoteTimeout, "", r.commandEnv(), ghPath, "auth", "status", "--hostname", "github.com")
	if res.err != nil {
		detail := "not logged in to github.com"
		if res.timedOut {
			detail = "gh auth status did not answer"
		} else if line := firstMeaningfulLine(res.output()); line != "" {
			detail += ": " + truncate(line, 120)
		}
		r.add(DoctorCheck{Section: section, Name: "gh", Status: missing, Detail: detail, Fix: "gh auth login"})
		return
	}
	r.add(DoctorCheck{Section: section, Name: "gh", Status: checkOK, Detail: "logged in to github.com (" + ghPath + ")"})
}

func (r *doctorRun) daemonFix() string {
	if r.env.goos == "darwin" {
		if _, err := os.Stat(r.launchAgentPath()); err != nil {
			return "git-auto-sync daemon enable"
		}
	}
	return "git-auto-sync daemon start"
}

func (r *doctorRun) checkDaemon(haveRepos bool) {
	const section = "daemon"
	path := r.env.socketPath
	if err := common.CheckSocketPath(path); err != nil {
		r.add(DoctorCheck{Section: section, Name: "socket path", Status: checkFail, Detail: err.Error(), Fix: "use a shorter " + common.SocketEnvVar + " or HOME"})
		return
	}

	down := checkWarn
	if haveRepos {
		down = checkFail
	}

	client, err := common.NewSocketClient(path)
	if err != nil {
		detail := "not running (nothing is listening on " + path + ")"
		if info, serr := os.Lstat(path); serr == nil && info.Mode()&os.ModeSocket != 0 {
			detail = "not running (stale socket at " + path + ")"
		}
		r.add(DoctorCheck{Section: section, Name: "daemon", Status: down, Detail: detail, Fix: r.daemonFix()})
	} else {
		client.Timeout = r.env.cmdTimeout
		resp, err := client.SendCommand(common.Request{Cmd: common.CmdVersion})
		client.Close()
		var v common.VersionResult
		switch {
		case errors.Is(err, common.ErrIncompatibleDaemon):
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkFail, Detail: "running, but it is an older version that does not speak protocol " + fmt.Sprint(common.ProtocolVersion), Fix: "install the daemon that matches this CLI (just install), then git-auto-sync daemon restart"})
		case err != nil:
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkFail, Detail: "the socket accepted a connection but did not answer: " + err.Error(), Fix: "git-auto-sync daemon restart"})
		case resp.Err() != nil:
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkFail, Detail: "version request failed: " + resp.Err().Error(), Fix: "git-auto-sync daemon restart"})
		case resp.Decode(&v) != nil || v.Protocol != common.ProtocolVersion:
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkFail, Detail: fmt.Sprintf("running with protocol %d, this CLI speaks %d", v.Protocol, common.ProtocolVersion), Fix: "install the daemon that matches this CLI (just install), then git-auto-sync daemon restart"})
		case v.Version != r.env.cliVersion && v.Version != "dev" && r.env.cliVersion != "dev":
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkWarn, Detail: fmt.Sprintf("running %s (protocol %d), but this CLI is %s", v.Version, v.Protocol, r.env.cliVersion), Fix: "git-auto-sync daemon restart"})
		default:
			r.add(DoctorCheck{Section: section, Name: "daemon", Status: checkOK, Detail: fmt.Sprintf("running %s, protocol %d", v.Version, v.Protocol)})
		}
	}

	if r.env.goos == "windows" {
		return
	}
	info, err := os.Lstat(path)
	if err != nil {
		return
	}
	switch {
	case info.Mode()&os.ModeSocket == 0:
		r.add(DoctorCheck{Section: section, Name: "socket permissions", Status: checkFail, Detail: path + " is not a socket", Fix: "move the file away and restart the daemon"})
	case info.Mode().Perm()&0o077 != 0:
		r.add(DoctorCheck{Section: section, Name: "socket permissions", Status: checkWarn, Detail: fmt.Sprintf("%s is %#o; other users can talk to your daemon", path, info.Mode().Perm()), Fix: "git-auto-sync daemon restart"})
	default:
		detail := fmt.Sprintf("%#o", info.Mode().Perm())
		dir := filepath.Dir(path)
		if filepath.Clean(dir) == filepath.Clean(r.env.stateDir) {
			if dinfo, err := os.Stat(dir); err == nil && dinfo.Mode().Perm()&0o077 != 0 {
				r.add(DoctorCheck{Section: section, Name: "socket permissions", Status: checkWarn, Detail: fmt.Sprintf("%s is %#o", dir, dinfo.Mode().Perm()), Fix: "chmod 700 " + shellQuote(dir)})
				return
			}
		}
		r.add(DoctorCheck{Section: section, Name: "socket permissions", Status: checkOK, Detail: detail})
	}
}

func (r *doctorRun) launchAgentPath() string {
	return filepath.Join(r.env.home, "Library", "LaunchAgents", launchAgentName+".plist")
}

func (r *doctorRun) checkLaunchAgent() {
	if r.env.goos != "darwin" {
		return
	}
	const section = "daemon"
	path := r.launchAgentPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkWarn, Detail: "not installed, so the daemon does not start at login", Fix: "git-auto-sync daemon enable"})
		return
	}
	if err != nil {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkFail, Detail: err.Error()})
		return
	}
	values, err := plistStrings(data)
	if err != nil {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkFail, Detail: "cannot parse " + path + ": " + err.Error(), Fix: "git-auto-sync daemon enable"})
		return
	}
	program := values["Program"]
	if program == "" {
		program = values["ProgramArguments"]
	}
	if program == "" {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkFail, Detail: path + " names no program", Fix: "git-auto-sync daemon enable"})
		return
	}
	pinfo, err := os.Stat(program)
	if err != nil || pinfo.IsDir() || pinfo.Mode()&0o111 == 0 {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkFail, Detail: "points at " + program + ", which does not exist or is not executable", Fix: "just install, then git-auto-sync daemon enable"})
		return
	}
	var notes []string
	if r.env.executable != "" {
		sibling := filepath.Join(filepath.Dir(r.env.executable), "git-auto-sync-daemon")
		if _, err := os.Stat(sibling); err == nil && common.ResolvePath(sibling) != common.ResolvePath(program) {
			notes = append(notes, "this CLI's daemon is "+sibling)
		}
	}
	if errPath := values["StandardErrorPath"]; errPath != "" && filepath.Dir(errPath) == filepath.Clean(r.env.home) {
		notes = append(notes, "it still logs to "+errPath+" (installed by an older version)")
	}
	if len(notes) > 0 {
		r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkWarn, Detail: "runs " + program + ", but " + strings.Join(notes, "; "), Fix: "git-auto-sync daemon enable"})
		return
	}
	r.add(DoctorCheck{Section: section, Name: "LaunchAgent", Status: checkOK, Detail: "runs " + program})
}

func plistStrings(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	values := map[string]string{}
	depth := 0
	key := ""
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "key":
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, err
				}
				depth--
				if depth == 2 {
					key = k
				}
			case "string":
				var v string
				if err := dec.DecodeElement(&v, &t); err != nil {
					return nil, err
				}
				depth--
				if key != "" && (depth == 2 || (depth == 3 && key == "ProgramArguments")) {
					if _, seen := values[key]; !seen {
						values[key] = strings.TrimSpace(v)
					}
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return values, nil
}

func (r *doctorRun) checkLogs() {
	const section = "logs"
	dir := filepath.Dir(r.env.logPath)
	if info, err := os.Stat(dir); err == nil {
		switch {
		case !info.IsDir():
			r.add(DoctorCheck{Section: section, Name: "log folder", Status: checkFail, Detail: dir + " is not a folder"})
		case dirWritable(dir) != nil:
			r.add(DoctorCheck{Section: section, Name: "log folder", Status: checkFail, Detail: dir + " is not writable", Fix: "chmod u+w " + shellQuote(dir)})
		default:
			r.add(DoctorCheck{Section: section, Name: "log folder", Status: checkOK, Detail: dir})
		}
	} else {
		parent := dir
		for {
			next := filepath.Dir(parent)
			if next == parent {
				break
			}
			parent = next
			if _, err := os.Stat(parent); err == nil {
				break
			}
		}
		if dirWritable(parent) != nil {
			r.add(DoctorCheck{Section: section, Name: "log folder", Status: checkFail, Detail: dir + " does not exist and " + parent + " is not writable"})
		} else {
			r.add(DoctorCheck{Section: section, Name: "log folder", Status: checkOK, Detail: dir + " (created when the daemon starts)"})
		}
	}

	var old []string
	var total int64
	for _, name := range []string{"git-auto-sync-daemon.err.log", "git-auto-sync-daemon.out.log"} {
		p := filepath.Join(r.env.home, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			old = append(old, fmt.Sprintf("%s (%s)", p, humanSize(info.Size())))
			total += info.Size()
		}
	}
	if len(old) > 0 {
		paths := make([]string, 0, len(old))
		for _, name := range []string{"git-auto-sync-daemon.err.log", "git-auto-sync-daemon.out.log"} {
			p := filepath.Join(r.env.home, name)
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, shellQuote(p))
			}
		}
		r.add(DoctorCheck{Section: section, Name: "old logs", Status: checkWarn, Detail: "logs from an older version, " + humanSize(total) + " in total: " + strings.Join(old, ", "), Fix: "once `git-auto-sync daemon logs` shows the new log, delete them: rm " + strings.Join(paths, " ")})
	}
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func shellSafe(r rune) bool {
	return strings.ContainsRune("/.-_~+@:", r) || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !shellSafe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func redactURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.LastIndex(rest, "@"); at >= 0 && at < strings.IndexAny(rest+"/", "/") {
			return u[:i+3] + "***@" + rest[at+1:]
		}
	}
	return u
}

func gitErrorLine(s string) string {
	last := ""
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		last = l
		for _, p := range []string{"fatal: ", "error: ", "ERROR: "} {
			if strings.HasPrefix(l, p) {
				return strings.TrimPrefix(l, p)
			}
		}
	}
	return last
}

func firstMeaningfulLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "X✗×-!"))
		if l != "" && !strings.HasSuffix(l, ":") && l != "github.com" {
			return l
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
