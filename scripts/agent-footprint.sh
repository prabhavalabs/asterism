#!/usr/bin/env bash
# agent-footprint.sh: measure asterism-agent's steady-state footprint.
#
#   scripts/agent-footprint.sh <minutes> [containers=50]
#
# Starts a hub (agent mode), a read-only Docker socket proxy, N idle alpine
# containers and the hardened agent container, all labelled
# asterism.test=1 and asterism.fp=1 and removed on exit. After a warm-up it
# measures, over <minutes>:
#   CPU      delta of the agent cgroup's cpu.stat usage_usec / wall time
#   RSS      docker stats MemUsage sampled for the whole window (max, and
#            p50 of the second half = steady state), plus the agent's own
#            VmRSS from /proc/<pid>/status and the cgroup's anon, file and
#            kernel memory at the end. docker stats includes page cache of
#            the agent binary when this cgroup was the first to fault it
#            in (up to ~5 MB, varies run to run); anon is the heap proper.
#   Network  delta of the agent container's eth0 tx bytes / wall time
#            (wire bytes incl. TCP/IP and HTTP/2 framing), and the hub's
#            bytes_received (frame payload) for comparison
# and fails if CPU > 0.5% of one core, steady RSS > 15 MB or tx > 2 KB/s.
#
# Nothing sleeps: waits block on log lines or on `docker stats` sampling
# bounded by timeout (or perl's alarm where coreutils timeout is missing).
#
# Images (build them first with `make agent-image` and `make e2e-agent`, or
# point at others): ASTERISM_AGENT_IMAGE (asterism-agent:it),
# ASTERISM_HUB_IMAGE (asterism-hub:it). ASTERISM_FP_ADAPTIVE=false disables
# idle interval stretching (the rate the agent uses while someone watches).
# ASTERISM_FP_MEMORY_LIMIT sets the agent's GOMEMLIMIT (image default 12MiB).
# ASTERISM_FP_OUT keeps the raw samples in that directory.
# ASTERISM_FP_PPROF=1 (with an image built with EXTRA_TAGS=pprof) also saves
# heap and 30 s CPU profiles of the agent at the end into ASTERISM_FP_OUT.
set -euo pipefail

MINUTES=${1:-}
N=${2:-50}
[[ "$MINUTES" =~ ^[0-9]+$ && "$MINUTES" -ge 1 ]] || {
	echo "usage: $0 <minutes> [containers=50]" >&2
	exit 2
}
[[ "$N" =~ ^[0-9]+$ ]] || { echo "containers must be a number" >&2; exit 2; }

AGENT_IMAGE=${ASTERISM_AGENT_IMAGE:-asterism-agent:it}
HUB_IMAGE=${ASTERISM_HUB_IMAGE:-asterism-hub:it}
ADAPTIVE=${ASTERISM_FP_ADAPTIVE:-true}
IDLE_IMAGE=alpine:3.20
HELPER_IMAGE=alpine:3.20
PROXY_IMAGE=wollomatic/socket-proxy:1
WARMUP=45 # seconds: first inventory snapshot, first keyframes
P=asterism-fp
LABELS=(--label asterism.test=1 --label asterism.fp=1)

# Targets (BLUEPRINT §16).
MAX_CPU_PCT=0.5
MAX_RSS_BYTES=$((15 * 1000 * 1000))
MAX_TX_BPS=2048

say() { printf '%s\n' "agent-footprint: $*" >&2; }

# run_for SECONDS CMD...: run CMD and stop it after SECONDS.
run_for() {
	local secs=$1
	shift
	if command -v timeout >/dev/null 2>&1; then
		timeout "$secs" "$@" || true
	elif command -v gtimeout >/dev/null 2>&1; then
		gtimeout "$secs" "$@" || true
	else
		# Go programs (docker) ignore SIGALRM, so send SIGTERM on the alarm.
		perl -e 'my $t = shift; my $p = fork // die "fork: $!";
			if (!$p) { exec @ARGV or exit 127 }
			$SIG{ALRM} = sub { kill "TERM", $p }; alarm $t;
			while (waitpid($p, 0) == -1 && $!{EINTR}) {} exit($? >> 8)' "$secs" "$@" || true
	fi
}

for img in "$AGENT_IMAGE" "$HUB_IMAGE"; do
	docker image inspect "$img" >/dev/null 2>&1 || {
		say "image $img not found: run 'make agent-image' (agent) and 'make e2e-agent' (hub), or set ASTERISM_AGENT_IMAGE / ASTERISM_HUB_IMAGE"
		exit 1
	}
done

# remove_rig deletes every container and the network of a previous or
# current run (by label), so an interrupted run never leaks resources.
remove_rig() {
	docker ps -aq --filter label=asterism.fp=1 --filter label=asterism.test=1 | xargs -r docker rm -f >/dev/null 2>&1 || true
	docker network rm "$P" >/dev/null 2>&1 || true
}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/asterism-fp.XXXXXX")
cleanup() {
	remove_rig
	if [ -n "${ASTERISM_FP_OUT:-}" ]; then
		mkdir -p "$ASTERISM_FP_OUT" && cp "$WORK"/*.log "$WORK"/*.txt "$WORK"/*.pprof "$ASTERISM_FP_OUT"/ 2>/dev/null || true
	fi
	rm -rf "$WORK"
}
remove_rig
trap cleanup EXIT INT TERM

# --- tokens and configs ------------------------------------------------------
gen() { docker run --rm "${LABELS[@]}" --entrypoint /usr/local/bin/asterism "$HUB_IMAGE" "$@"; }
API_OUT=$(gen token generate --name fp --scope read)
AGENT_OUT=$(gen agent token create --host fp-host)
API_TOKEN=$(printf '%s\n' "$API_OUT" | awk '/^  ast_/ {print $1; exit}')
API_SHA=$(printf '%s\n' "$API_OUT" | awk -F'"' '/^sha256/ {print $2; exit}')
AGENT_TOKEN=$(printf '%s\n' "$AGENT_OUT" | awk '/^  asta_/ {print $1; exit}')
AGENT_SHA=$(printf '%s\n' "$AGENT_OUT" | awk -F'"' '/^sha256/ {print $2; exit}')
[ -n "$API_TOKEN" ] && [ -n "$AGENT_TOKEN" ] || { say "could not create tokens"; exit 1; }

cat >"$WORK/hub.toml" <<EOF
[server]
listen = "0.0.0.0:7700"
[collect]
mode = "agent"
[auth]
admin_email = "fp@example.com"
admin_password = "fp-admin-password"
[[auth.tokens]]
name = "fp"
sha256 = "$API_SHA"
scopes = ["read"]
[[agents]]
name = "fp-host"
sha256 = "$AGENT_SHA"
EOF
printf '%s\n' "$AGENT_TOKEN" >"$WORK/agent.token"
cat >"$WORK/agent.yaml" <<EOF
version: 1
hub:
  url: http://hub:7700
  tls: {insecure_dev: true}
host:
  name: fp-host
intervals:
  adaptive: $ADAPTIVE
EOF
chmod 0644 "$WORK"/*

# --- containers ----------------------------------------------------------------
docker network create "${LABELS[@]}" "$P" >/dev/null
say "starting $N idle containers"
for i in $(seq 1 "$N"); do
	docker run -d "${LABELS[@]}" --name "$P-idle-$i" --network "$P" "$IDLE_IMAGE" tail -f /dev/null >/dev/null
done
docker run -d "${LABELS[@]}" --name "$P-hub" --network "$P" --network-alias hub \
	-v "$WORK/hub.toml:/etc/asterism/hub.toml:ro" -p 127.0.0.1::7700 "$HUB_IMAGE" >/dev/null
docker run -d "${LABELS[@]}" --label asterism.protected=true --name "$P-proxy" --network "$P" --network-alias socket-proxy \
	--read-only --cap-drop ALL --security-opt no-new-privileges:true --user 0:0 \
	-v /var/run/docker.sock:/var/run/docker.sock:ro "$PROXY_IMAGE" \
	-loglevel=WARN -listenip=0.0.0.0 -allowfrom="$P-agent" -allowHEAD='^/_ping$' \
	-allowGET='^(/v1\.[0-9]+)?/(_ping|info|events|containers/json|containers/[A-Za-z0-9_.-]+/(json|logs)|images/json|volumes|networks|system/df)$' >/dev/null
PPROF_ENV=()
[ "${ASTERISM_FP_PPROF:-}" = 1 ] && PPROF_ENV=(-e ASTERISM_AGENT_PPROF=127.0.0.1:6060)
[ -n "${ASTERISM_FP_MEMORY_LIMIT:-}" ] && PPROF_ENV+=(-e "GOMEMLIMIT=$ASTERISM_FP_MEMORY_LIMIT")
docker run -d "${LABELS[@]}" --label asterism.self=true --name "$P-agent" --network "$P" "${PPROF_ENV[@]}" \
	--read-only --user 65532:65532 --cap-drop ALL --security-opt no-new-privileges:true \
	--memory 64m --pids-limit 64 \
	-e ASTERISM_AGENT_TOKEN_FILE=/etc/asterism/agent.token -e ASTERISM_AGENT_DOCKER_ENDPOINT=tcp://socket-proxy:2375 \
	-v /proc:/host/proc:ro -v /sys/fs/cgroup:/host/sys/fs/cgroup:ro \
	-v "$WORK/agent.yaml:/etc/asterism/agent.yaml:ro" -v "$WORK/agent.token:/etc/asterism/agent.token:ro" \
	"$AGENT_IMAGE" >/dev/null

HUB_URL="http://$(docker port "$P-hub" 7700/tcp | head -1)"

list_agents() {
	curl -fsS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
		-d '{}' "$HUB_URL/asterism.v1.AgentAdminService/ListAgents"
}

# Counters of the agent container, read from the host PID and cgroup
# namespaces by a throwaway helper (the agent image has no shell).
counters() {
	local pid
	pid=$(docker inspect -f '{{.State.Pid}}' "$P-agent")
	docker run --rm "${LABELS[@]}" --pid=host --cgroupns=host -v /sys/fs/cgroup:/cg:ro "$HELPER_IMAGE" sh -c '
		pid='"$pid"'
		cg=$(sed -n "s/^0:://p" /proc/$pid/cgroup)
		usec=$(awk "/^usage_usec/ {print \$2}" /cg$cg/cpu.stat)
		tx=$(awk -F"[: ]+" "/eth0:/ {print \$11}" /proc/$pid/net/dev)
		rss=$(awk "/^VmRSS/ {print \$2*1024}" /proc/$pid/status)
		ranon=$(awk "/^RssAnon/ {print \$2*1024}" /proc/$pid/status)
		rfile=$(awk "/^RssFile/ {print \$2*1024}" /proc/$pid/status)
		anon=$(awk "/^anon / {print \$2}" /cg$cg/memory.stat)
		file=$(awk "/^file / {print \$2}" /cg$cg/memory.stat)
		kern=$(awk "/^kernel / {print \$2}" /cg$cg/memory.stat)
		echo "$usec $tx $rss $anon $ranon $rfile $file $kern"'
}

say "warming up for ${WARMUP}s (sampling, not sleeping)"
run_for "$WARMUP" docker stats "$P-agent" --format '{{.MemUsage}}' >"$WORK/warmup.log" 2>/dev/null
docker logs "$P-agent" 2>&1 | grep -q 'connected to hub' || { docker logs "$P-agent" >&2; say "agent did not connect"; exit 1; }

read -r USEC0 TX0 _ _ _ _ _ _ <<<"$(counters)"
T0=$(date +%s)
HUB0=$(list_agents | sed -n 's/.*"bytesReceived":"\{0,1\}\([0-9]*\).*/\1/p')
say "measuring for $MINUTES minute(s) with $N containers (adaptive=$ADAPTIVE)"
run_for "$((MINUTES * 60))" docker stats "$P-agent" --format '{{.MemUsage}}' >"$WORK/rss.log" 2>/dev/null
read -r USEC1 TX1 VMRSS ANON RSSANON RSSFILE CGFILE CGKERN <<<"$(counters)"
T1=$(date +%s)
AGENTS_JSON=$(list_agents)
HUB1=$(printf '%s' "$AGENTS_JSON" | sed -n 's/.*"bytesReceived":"\{0,1\}\([0-9]*\).*/\1/p')
printf '%s\n' "$AGENTS_JSON" >"$WORK/agents.txt"
SELF_RSS=$(printf '%s' "$AGENTS_JSON" | sed -n 's/.*"rssBytes":"\{0,1\}\([0-9]*\).*/\1/p')
CONTAINERS_SEEN=$(curl -fsS -H "Authorization: Bearer $API_TOKEN" -H 'Content-Type: application/json' \
	-d '{"query":"asterism-fp-idle","page":{"pageSize":500}}' "$HUB_URL/asterism.v1.DockerService/ListContainers" |
	grep -o '"name":"asterism-fp-idle-[0-9]*"' | sort -u | wc -l | tr -d ' ')

if [ "${ASTERISM_FP_PPROF:-}" = 1 ]; then
	say "saving heap and CPU profiles"
	docker run --rm "${LABELS[@]}" --network "container:$P-agent" -v "$WORK:/out" "$HELPER_IMAGE" sh -c \
		'wget -q -O /out/heap.pprof http://127.0.0.1:6060/debug/pprof/heap &&
		 wget -q -O /out/allocs.pprof http://127.0.0.1:6060/debug/pprof/allocs &&
		 wget -q -O /out/cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"' || say "profiles unavailable (image built without EXTRA_TAGS=pprof?)"
fi

# docker stats MemUsage, e.g. "6.418MiB / 64MiB" (with screen-clearing
# escapes between refreshes when not on a TTY): one value per sample, bytes.
grep -oE '[0-9.]+(B|KiB|MiB|GiB|kB|MB|GB) /' "$WORK/rss.log" | awk '
	{ v=$1; u=v; sub(/[0-9.]+/, "", u); sub(/[A-Za-z]+/, "", v)
	  m = (u=="KiB")?1024:(u=="MiB")?1048576:(u=="GiB")?1073741824:(u=="kB")?1e3:(u=="MB")?1e6:(u=="GB")?1e9:1
	  printf "%d\n", v*m }' >"$WORK/rss.txt"

SAMPLES=$(wc -l <"$WORK/rss.txt" | tr -d ' ')
[ "$SAMPLES" -gt 0 ] || { say "no docker stats samples"; exit 1; }
RSS_MAX=$(sort -n "$WORK/rss.txt" | tail -1)
RSS_P50=$(tail -n "$(((SAMPLES + 1) / 2))" "$WORK/rss.txt" | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}')

DT=$((T1 - T0))
awk -v du=$((USEC1 - USEC0)) -v dtx=$((TX1 - TX0)) -v dh=$((${HUB1:-0} - ${HUB0:-0})) -v dt="$DT" \
	-v rmax="$RSS_MAX" -v rp50="$RSS_P50" -v vmrss="$VMRSS" -v anon="$ANON" -v ranon="$RSSANON" -v rfile="$RSSFILE" -v cgfile="${CGFILE:-0}" -v cgkern="${CGKERN:-0}" -v self="${SELF_RSS:-0}" \
	-v n="$N" -v seen="$CONTAINERS_SEEN" -v samples="$SAMPLES" -v adaptive="$ADAPTIVE" \
	-v maxcpu="$MAX_CPU_PCT" -v maxrss="$MAX_RSS_BYTES" -v maxtx="$MAX_TX_BPS" '
function mb(x) { return sprintf("%.2f MB", x / 1e6) }
function verdict(ok) { return ok ? "PASS" : "FAIL" }
BEGIN {
	cpu = du / (dt * 1e6) * 100; tx = dtx / dt; hub = dh / dt
	printf "\nasterism-agent footprint: %d containers (%d seen by the hub), %ds window, %d RSS samples, adaptive=%s\n\n", n, seen, dt, samples, adaptive
	printf "| Metric | Measured | Target | Result |\n|---|---|---|---|\n"
	printf "| CPU (cgroup usage_usec) | %.3f%% of one core | <= %.1f%% | %s |\n", cpu, maxcpu, verdict(cpu <= maxcpu)
	printf "| Memory, steady (docker stats p50, 2nd half) | %s | <= %s | %s |\n", mb(rp50), mb(maxrss), verdict(rp50 <= maxrss)
	printf "| Memory, max (docker stats) | %s | | |\n", mb(rmax)
	printf "| VmRSS at end (/proc/<pid>/status) | %s (anon %s + file %s) | | |\n", mb(vmrss), mb(ranon), mb(rfile)
	printf "| cgroup at end: anon / file / kernel | %s / %s / %s | | |\n", mb(anon), mb(cgfile), mb(cgkern)
	printf "| Agent self-reported RSS | %s | | |\n", mb(self)
	printf "| Network tx (eth0, wire) | %.0f B/s | <= %d B/s | %s |\n", tx, maxtx, verdict(tx <= maxtx)
	printf "| Hub bytes_received (frame payload) | %.0f B/s | | |\n", hub
	fail = !(cpu <= maxcpu && rp50 <= maxrss && tx <= maxtx)
	exit fail
}'
