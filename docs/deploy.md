# Deploying beacon in a FreeBSD jail

The server runs in its own jail as the unprivileged user `beacon`, behind a
reverse proxy (Caddy) that terminates TLS. Needs FreeBSD 13 or newer.

In the examples, the beacon jail has the IP `10.0.0.5`, the public name is
`beacon.example.org`, and Radicale is at `https://dav.example.org/`.
Commands marked "jail" run as root inside the beacon jail.

## 1. Build

On the laptop, in the beacon repository:

```sh
nix build .#beacon-freebsd-amd64     # result/bin/beacon is a static FreeBSD binary
```

Copy both files into the jail: `result/bin/beacon` to `/tmp/beacon` and
`deploy/rc.d/beacon` to `/tmp/beacon.rc`.

## 2. Install (jail)

```sh
pw useradd -n beacon -c "beacon server" -d /nonexistent -s /usr/sbin/nologin
install -m 0755 -o root -g wheel /tmp/beacon /usr/local/bin/beacon
install -m 0755 -o root -g wheel /tmp/beacon.rc /usr/local/etc/rc.d/beacon
install -d -m 0755 -o root -g wheel /usr/local/etc/beacon
```

On every start, the rc.d script (running as root) creates what the `beacon`
user needs: `/var/db/beacon` (mode 0700), `/var/run/beacon` for the pidfile
(emptied when the jail restarts) and `/var/log/beacon.log` (mode 0640). It
then starts daemon(8) as `beacon` through rc.subr, so nothing runs as root.

## 3. Settings and secrets (jail)

Non-secret settings go in the env file. It is read by the rc.d script as
root, so it must belong to root and must not be writable by `beacon`:

```sh
cat > /usr/local/etc/beacon/beacon.env <<'EOF'
BEACON_CALDAV_URL=https://dav.example.org/
BEACON_CALDAV_USER=pascal
BEACON_CALDAV_PASSWORD_CMD="cat /usr/local/etc/beacon/radicale.pass"
BEACON_LISTS=Todo
BEACON_DEFAULT_LIST=Todo
EOF
chmod 0644 /usr/local/etc/beacon/beacon.env
```

Secrets go in files owned by `beacon` with mode 0600. A fresh API token:

```sh
(umask 077 && openssl rand -hex 32 > /usr/local/etc/beacon/token)
chown beacon:beacon /usr/local/etc/beacon/token
```

The Radicale password (create the file empty, then type the password into
it with an editor, so it never lands in your shell history):

```sh
install -m 0600 -o beacon -g beacon /dev/null /usr/local/etc/beacon/radicale.pass
ee /usr/local/etc/beacon/radicale.pass
```

beacon refuses to start if other users can read the token file.

Set the jail's time zone to yours, so "overdue" and "due today" are right:

```sh
tzsetup
```

## 4. Enable and start (jail)

```sh
sysrc beacon_enable=YES
sysrc beacon_listen=10.0.0.5:8080
service beacon start
```

`beacon_listen` defaults to `127.0.0.1:8080`, which only works if Caddy runs
in the same jail. If Caddy runs in a different jail, beacon must listen on
this jail's IP. Then allow only the proxy to reach it, for example with pf
on the host (proxy at `10.0.0.2`):

```
pass in quick proto tcp from 10.0.0.2 to 10.0.0.5 port 8080
block in quick proto tcp to 10.0.0.5 port 8080
```

Other rc.conf settings (`beacon_user`, `beacon_db`, `beacon_env_file`,
`beacon_token_file`, `beacon_logfile`, `beacon_pidfile`) are described at the
top of the rc.d script. The log is `/var/log/beacon.log`. To rotate it, add
`/usr/local/etc/newsyslog.conf.d/beacon.conf`. newsyslog recreates the log
owned by `beacon`, then sends SIGHUP to daemon(8), which reopens it:

```
/var/log/beacon.log  beacon:beacon  640  7  1000  *  JC  /var/run/beacon/beacon.pid  1
```

## 5. Reverse proxy (Caddy)

```
beacon.example.org {
	reverse_proxy 10.0.0.5:8080

	# Every answer is about the current state; never store it.
	header Cache-Control "no-store"
}
```

Caddy gets the TLS certificate itself, passes the `Authorization` header
through unchanged and does not cache. Its access logs, if enabled, hide the
`Authorization` header.

## 6. Verify

In the jail:

```sh
service beacon status
fetch -qo - http://10.0.0.5:8080/healthz      # prints: ok
tail /var/log/beacon.log
```

From the laptop:

```sh
curl https://beacon.example.org/healthz                                # ok
curl -s -o /dev/null -w '%{http_code}\n' https://beacon.example.org/current   # 401
```

Copy the token to the laptop over SSH (adjust host and jail name), keeping it
private there too:

```sh
mkdir -p ~/.config/beacon
(umask 077 && ssh root@odin jexec beacon cat /usr/local/etc/beacon/token > ~/.config/beacon/token)

export BEACON_SERVER_URL=https://beacon.example.org
export BEACON_TOKEN_FILE=~/.config/beacon/token
t focus
```

## 7. Update to a new version

On the laptop: `git pull`, then `nix build .#beacon-freebsd-amd64` and copy
`result/bin/beacon` into the jail. In the jail:

```sh
cp /usr/local/bin/beacon /usr/local/bin/beacon.old     # for a quick rollback
install -m 0755 -o root -g wheel /tmp/beacon /usr/local/bin/beacon
service beacon restart
fetch -qo - http://10.0.0.5:8080/healthz
```

If `deploy/rc.d/beacon` changed, copy and install it again as in steps 1 and 2.
The focus state in `/var/db/beacon` is kept. To roll back, put
`beacon.old` back and restart.
