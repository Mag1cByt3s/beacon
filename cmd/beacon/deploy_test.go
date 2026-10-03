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

// fakeRCSubr stands in for FreeBSD's /etc/rc.subr: load_rc_config reads a
// test rc.conf, and run_rc_command runs the start hook and then prints what
// would be started and with which BEACON_* environment.
const fakeRCSubr = `
load_rc_config() { . "$TEST_RC_CONF"; }
run_rc_command() {
	$start_precmd || exit 1
	echo "command=$command"
	echo "args=$command_args"
	echo "pidfile=$pidfile"
	echo "required=$required_files"
	env | grep '^BEACON_' | sort
}
`

// TestRCScriptStart runs the rc.d script on Linux with a fake rc.subr. It
// checks the parts that are plain sh: defaults, rc.conf values winning
// over the env file, and creating the database directory.
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
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	script, err := os.ReadFile(rcScript)
	if err != nil {
		t.Fatal(err)
	}
	subr := write("rc.subr", fakeRCSubr)
	testScript := write("beacon", strings.Replace(string(script), "/etc/rc.subr", subr, 1))
	envFile := write("beacon.env", "BEACON_CALDAV_URL=https://dav.example.org/\n"+
		"BEACON_CALDAV_PASSWORD_CMD=\"cat /usr/local/etc/beacon/radicale.pass\"\n"+
		"BEACON_LISTEN=0.0.0.0:9999\n") // rc.conf must win over this
	dbDir := filepath.Join(dir, "db", "beacon")
	rcConf := write("rc.conf", strings.Join([]string{
		"beacon_enable=YES",
		"beacon_user=" + me.Username,
		"beacon_group=" + group.Name,
		"beacon_listen=10.0.0.5:8080",
		"beacon_db=" + filepath.Join(dbDir, "beacon.db"),
		"beacon_env_file=" + envFile,
		"beacon_token_file=/usr/local/etc/beacon/token",
	}, "\n")+"\n")

	cmd := exec.Command("sh", testScript, "start")
	cmd.Env = append(os.Environ(), "TEST_RC_CONF="+rcConf)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}
	got := string(out)

	for _, want := range []string{
		"command=/usr/sbin/daemon",
		"pidfile=/var/run/beacon.pid",
		"-r -R 5", "-P /var/run/beacon.pid", "-o /var/log/beacon.log",
		"-u " + me.Username + " /usr/local/bin/beacon",
		"required=" + envFile + " /usr/local/etc/beacon/token",
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
	if strings.Contains(got, "0.0.0.0:9999") {
		t.Errorf("the env file's BEACON_LISTEN won over rc.conf:\n%s", got)
	}

	info, err := os.Stat(dbDir)
	if err != nil {
		t.Fatalf("database directory was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("database directory mode = %o, want 700", perm)
	}
}
