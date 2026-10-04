package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

const rcScript = "../../deploy/rc.d/beacon"

func TestRCScriptSyntax(t *testing.T) {
	if out, err := exec.Command("sh", "-n", rcScript).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

// fakeRCSubr stands in for FreeBSD's /etc/rc.subr. Like the real one (see
// run_rc_command in libexec/rc/rc.subr), it runs start_precmd as root and
// then, because ${name}_user is set, the command through
// "su -m $user -c ...". The stand-in su checks for -m, records the user and
// runs the command in a fresh child shell, so only exported variables get
// through, as with the real su -m.
const fakeRCSubr = `
load_rc_config() { . "$TEST_RC_CONF"; }
su() {
	[ "$1" = "-m" ] || { echo "su without -m" >&2; return 1; }
	echo "su_user=$2"
	sh -c "$4"
}
run_rc_command() {
	eval _user=\$${name}_user
	$start_precmd || exit 1
	echo "pidfile=$pidfile"
	echo "procname=$procname"
	echo "required=$required_files"
	su -m "$_user" -c "$command $command_args"
}
`

// fakeDaemon stands in for daemon(8): it prints the arguments it got and
// the BEACON_* environment that reached it.
const fakeDaemon = `#!/bin/sh
echo "daemon args: $*"
env | grep '^BEACON_' | sort
`

// TestRCScriptStart runs the rc.d script on Linux with a fake rc.subr and
// daemon. It checks what is plain sh: the arguments for daemon, rc.conf
// values winning over the env file, the settings reaching beacon through
// su, and the directories and files beacon_user needs.
func TestRCScriptStart(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skipf("no current user: %v", err)
	}
	group, err := user.LookupGroupId(me.Gid)
	if err != nil {
		t.Skipf("no current group: %v", err)
	}

	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}

	script, err := os.ReadFile(rcScript)
	if err != nil {
		t.Fatal(err)
	}
	subr := write("rc.subr", fakeRCSubr, 0o644)
	daemon := write("daemon", fakeDaemon, 0o755)
	text := strings.Replace(string(script), "/etc/rc.subr", subr, 1)
	// Only the command is replaced; procname must stay the real daemon.
	text = strings.Replace(text, `command="/usr/sbin/daemon"`, `command="`+daemon+`"`, 1)
	testScript := write("beacon", text, 0o755)

	envFile := write("beacon.env", "BEACON_CALDAV_URL=https://dav.example.org/\n"+
		"BEACON_CALDAV_PASSWORD_CMD=\"cat /usr/local/etc/beacon/radicale.pass\"\n"+
		"BEACON_LISTEN=0.0.0.0:9999\n", 0o644) // rc.conf must win over this
	dbDir := filepath.Join(dir, "db", "beacon")
	runDir := filepath.Join(dir, "run", "beacon")
	logFile := filepath.Join(dir, "beacon.log")
	rcConf := write("rc.conf", strings.Join([]string{
		"beacon_enable=YES",
		"beacon_user=" + me.Username,
		"beacon_group=" + group.Name,
		"beacon_listen=10.0.0.5:8080",
		"beacon_db=" + filepath.Join(dbDir, "beacon.db"),
		"beacon_env_file=" + envFile,
		"beacon_token_file=/usr/local/etc/beacon/token",
		"beacon_logfile=" + logFile,
		"beacon_pidfile=" + filepath.Join(runDir, "beacon.pid"),
	}, "\n")+"\n", 0o644)

	start := func() string {
		t.Helper()
		cmd := exec.Command("sh", testScript, "start")
		cmd.Env = append(os.Environ(), "TEST_RC_CONF="+rcConf)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("start: %v\n%s", err, out)
		}
		return string(out)
	}
	got := start()

	for _, want := range []string{
		"su_user=" + me.Username,
		"procname=/usr/sbin/daemon",
		"pidfile=" + filepath.Join(runDir, "beacon.pid"),
		"required=" + envFile + " /usr/local/etc/beacon/token",
		"daemon args: -f -r -R 5 -H -P " + filepath.Join(runDir, "beacon.pid") + " -o " + logFile + " -t beacon /usr/local/bin/beacon",
		// Seen by the child process, so exported:
		"BEACON_CALDAV_URL=https://dav.example.org/",
		"BEACON_CALDAV_PASSWORD_CMD=cat /usr/local/etc/beacon/radicale.pass",
		"BEACON_LISTEN=10.0.0.5:8080",
		"BEACON_DB=" + filepath.Join(dbDir, "beacon.db"),
		"BEACON_TOKEN_FILE=/usr/local/etc/beacon/token",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, " -u ") {
		t.Errorf("daemon got -u, but it already runs as beacon_user:\n%s", got)
	}
	if strings.Contains(got, "0.0.0.0:9999") {
		t.Errorf("the env file's BEACON_LISTEN won over rc.conf:\n%s", got)
	}

	wantMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s was not created: %v", path, err)
			return
		}
		if perm := info.Mode().Perm(); perm != want {
			t.Errorf("%s mode = %o, want %o", path, perm, want)
		}
	}
	wantMode(dbDir, 0o700)
	wantMode(runDir, 0o755)
	wantMode(logFile, 0o640)

	// On the next start (say after a jail restart, which empties /var/run)
	// the pidfile directory is created again, and a log file with the wrong
	// mode is fixed.
	os.RemoveAll(runDir)
	os.Chmod(logFile, 0o600)
	start()
	wantMode(runDir, 0o755)
	wantMode(logFile, 0o640)
}

func TestRCScriptDefaults(t *testing.T) {
	script, err := os.ReadFile(rcScript)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`: "${beacon_pidfile:=/var/run/beacon/beacon.pid}"`,
		`: "${beacon_logfile:=/var/log/beacon.log}"`,
		`procname="/usr/sbin/daemon"`,
	} {
		if !strings.Contains(string(script), want) {
			t.Errorf("rc.d script lacks %q", want)
		}
	}
}
