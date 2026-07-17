#!/usr/bin/env sh
# Fails on any govulncheck finding whose advisory is not in ALLOW.
#
# ALLOW holds accepted-and-unfixable advisories only:
# - GO-2026-5746/5668/5617/4887/4883: daemon-side moby vulnerabilities in
#   github.com/docker/docker, whose module tags stopped at v28.5.2 so no fixed
#   version exists to upgrade to. dockbrr imports only the client SDK; the
#   vulnerable code paths run in the Docker daemon, not here. Revisit when the
#   SDK moves to the maintained moby v29 client module.
# - GO-2026-5932: golang.org/x/crypto/openpgp is unmaintained-by-design with
#   no fixed version; module-level finding only. dockbrr uses x/crypto solely
#   for argon2 and never imports openpgp.
set -eu

ALLOW="GO-2026-5746 GO-2026-5668 GO-2026-5617 GO-2026-4887 GO-2026-4883 GO-2026-5932"

out=$(mktemp)
trap 'rm -f "$out"' EXIT
go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... > "$out"

found=$(jq -r 'select(.finding != null) | .finding.osv' "$out" | sort -u)

new=""
for id in $found; do
  case " $ALLOW " in
    *" $id "*) ;;
    *) new="$new $id" ;;
  esac
done

if [ -n "$new" ]; then
  echo "govulncheck: NEW vulnerabilities not in allowlist:$new" >&2
  echo "Details: go run golang.org/x/vuln/cmd/govulncheck@latest ./..." >&2
  exit 1
fi
echo "govulncheck: ok (allowlisted, unfixable: $(echo $found | tr '\n' ' '))"
