#!/usr/bin/env bash
# Use only a fresh GitHub-hosted Docker runner. No local or production fallback.
set -euo pipefail
umask 077
if [[ ${GITHUB_ACTIONS:-} != true || ${RUNNER_ENVIRONMENT:-} != github-hosted || $(uname -sm) != 'Linux x86_64' ]]; then
  echo 'This pilot requires a GitHub-hosted Linux x86_64 runner.' >&2
  exit 1
fi
unset DOCKER_HOST DOCKER_CONTEXT
d() { docker --context default "$@"; }
baseline=$(d ps -aq) || exit 1
if [[ -n $baseline ]]; then
  echo 'Refusing to run on a Docker daemon with existing containers.' >&2
  exit 1
fi
root=$(mktemp -d "$HOME/brine-released-pair.XXXXXX")
volume=${root##*/}
cleanup() {
  if [[ -n ${agent_pid:-} ]]; then kill "$agent_pid" 2>/dev/null || true; wait "$agent_pid" 2>/dev/null || true; fi
  if [[ -f $root/container-id ]]; then d rm -fv "$( < "$root/container-id" )" >/dev/null 2>&1 || true; fi
  d volume rm "$volume" >/dev/null 2>&1 || true
  rm -rf -- "$root"
}
trap cleanup EXIT
image=ghcr.io/umayr/brine@sha256:72ac691e8998c18a91b98c6a95f50bd8bf676109529593e6356de34e5430c5aa
dill_sha=e27c4e6597f5b8fd12518a82d2f3e59d87ca59625f43bbdda6cc47fa85943cc0
pkl_sha=618f13955d755cafbfe8c9cba1d27635848cd49dbc6abffd398d2751db1231bf
curl -fsSL --retry 3 https://github.com/umayr/dill/releases/download/v0.8.1/dill-linux-amd64 -o "$root/dill"
curl -fsSL --retry 3 https://github.com/apple/pkl/releases/download/0.31.1/pkl-linux-amd64 -o "$root/pkl"
printf '%s  %s\n%s  %s\n' "$dill_sha" "$root/dill" "$pkl_sha" "$root/pkl" | sha256sum --check
chmod 700 "$root/dill" "$root/pkl"
[[ $("$root/dill" version) == v0.8.1 ]]
echo "Brine fixture image: $image; Dill asset sha256:$dill_sha"
d pull "$image"
d pull docker.io/library/alpine:3.22
mkdir -m 700 "$root"/{stages,catalog,coordination,operations}
d run --rm --entrypoint /bin/mkdir -v "$volume:/data" "$image" -m 0700 /data/remote
d run --rm -v "$volume:/data" "$image" --init-remote-registry --remote-registry-dir /data/remote
"$root/dill" agent init-https --dir "$root/agent"
export PILOT_AGENT_ADDR=127.0.0.1:17641 PILOT_BRINE_URL=http://127.0.0.1:17642
export PILOT_PAIRING="$root/pairing.json" PILOT_CHECKPOINT="$root/checkpoint.json"
export PILOT_GIT_SOURCE="$root/git-source"
git init -q -b main "$PILOT_GIT_SOURCE"
printf 'config {\n' > "$PILOT_GIT_SOURCE/compose.pkl"
git -C "$PILOT_GIT_SOURCE" add compose.pkl
git -C "$PILOT_GIT_SOURCE" -c user.name=Pilot -c user.email=pilot@example.invalid commit -qm 'Synthetic invalid project'
export BRINE_AUTH_PASSWORD=$(openssl rand -hex 24) BRINE_API_TOKEN=$(openssl rand -hex 24)
printf '::add-mask::%s\n::add-mask::%s\n' "$BRINE_AUTH_PASSWORD" "$BRINE_API_TOKEN"
engine_id=$(< /proc/sys/kernel/random/uuid)
daemon_id=$(d info --format '{{.ID}}')
[[ -n $daemon_id ]] || { echo 'Disposable Docker daemon did not report an ID' >&2; exit 1; }
agent=( "$root/dill" agent --https-dir "$root/agent" --https-listen "$PILOT_AGENT_ADDR"
        --engine-socket /var/run/docker.sock --engine-id "$engine_id" --generation 1
        --daemon-id "$daemon_id" --https-staging-root "$root/stages"
        --https-staging-catalog "$root/catalog" --https-preview-state "$root/coordination"
        --https-preview-pkl "$root/pkl" --https-apply-state "$root/operations" )
echo 'Exporting pairing for the disposable native Dill agent'
"${agent[@]}" --print-https-pairing > "$PILOT_PAIRING"
"${agent[@]}" > "$root/agent.log" 2>&1 &
agent_pid=$!
d run --detach --network host --cidfile "$root/container-id" -v "$volume:/data" \
  -v "$PILOT_GIT_SOURCE:/pilot-git:ro" \
  -v /var/run/docker.sock:/var/run/docker.sock -e BRINE_AUTH_PASSWORD -e BRINE_API_TOKEN "$image" \
  --bind 127.0.0.1 --port 17642 --remote-enabled --remote-timeout 20s --remote-registry-dir /data/remote >/dev/null
for _ in {1..30}; do
  kill -0 "$agent_pid" || { echo 'Native Dill agent exited during startup' >&2; exit 1; }
  if curl -fsS "$PILOT_BRINE_URL/api/health" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -fsS "$PILOT_BRINE_URL/api/health" >/dev/null
node scripts/released-pair-pilot.cjs
kill "$agent_pid"
wait "$agent_pid" || [[ $? == 143 ]]
unset agent_pid
"${agent[@]}" > "$root/agent-restart.log" 2>&1 &
agent_pid=$!
for _ in {1..30}; do
  kill -0 "$agent_pid" || { echo 'Native Dill agent exited during restart' >&2; exit 1; }
  if curl -ksS --max-time 1 "https://$PILOT_AGENT_ADDR/" >/dev/null 2>&1; then break; fi
  sleep 1
done
curl -ksS --max-time 1 "https://$PILOT_AGENT_ADDR/" >/dev/null
node scripts/released-pair-pilot.cjs --after-restart
