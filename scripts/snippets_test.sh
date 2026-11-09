#!/bin/sh
# Every command the site and the installation guide offer to copy, actually
# run.
#
#   sh scripts/snippets_test.sh
#
# This exists because the download page once handed people a block that read
# checksums.txt without ever telling them to download it, and named one
# platform's binary so that following it on a Mac produced "exec format
# error". Both were obvious the moment anybody ran them, and nobody had.
#
# Blocks that would change the machine -- sudo, a package manager, docker,
# brew, nix -- stop the run at that line: what comes before them is still
# checked, which is the downloading and verifying that went wrong.
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
pass=0
fail=0
skip=0

# Pull every shell block out of the pages and the guide, one file per block,
# with the HTML entities turned back into the characters they stand for.
python3 - "$root" "$work" <<'PY'
import glob, html, os, re, sys

root, work = sys.argv[1], sys.argv[2]
n = 0
blocks = []
for page in sorted(glob.glob(os.path.join(root, "site", "*.html"))):
    for m in re.finditer(r"<pre>.*?<code>(.*?)</code></pre>", open(page).read(), re.S):
        blocks.append((os.path.basename(page), html.unescape(re.sub(r"<[^>]+>", "", m.group(1)))))
for doc in (os.path.join(root, "docs", "INSTALL.md"),):
    for m in re.finditer(r"```sh\n(.*?)```", open(doc).read(), re.S):
        blocks.append((os.path.basename(doc), m.group(1)))
for name, body in blocks:
    n += 1
    with open(os.path.join(work, "block-%02d.sh" % n), "w") as f:
        f.write("# from %s\n%s" % (name, body))
print("%d blocks" % n)
PY

# The blocks ask the releases API without a token, exactly as a reader does,
# and that allowance is sixty requests an hour per address. Running out is not
# a broken command, so say so and check only what parses. /rate_limit does not
# itself count against the limit.
budget="$(curl --proto '=https' -fsSL https://api.github.com/rate_limit 2>/dev/null |
	tr ',' '\n' | sed -n 's/.*"remaining"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)"
network=yes
if [ "${budget:-1}" -lt 4 ] 2>/dev/null; then
	network=no
	echo "snippets: the GitHub API allowance is spent, so only parsing is checked"
fi

# stops names the commands that end a run: they install things, need a daemon,
# want a password, or need a signed-in gh. Everything above them is still
# exercised, which is the downloading and verifying that went wrong.
# The slashes are escaped because this is used as a sed address, where an
# unescaped one ends the pattern.
stops='^[[:space:]]*(sudo|brew|docker|nix|apt|dnf|zypper|apk|pacman|dpkg|rpm|go install|xattr|gh |(\.\/)?siltide)'

for block in "$work"/block-*.sh; do
	name="$(sed -n '1s/^# from //p' "$block")"
	label="$name $(basename "$block" .sh | sed 's/block-//')"

	if ! sh -n "$block" 2>"$work/err"; then
		echo "FAIL $label does not parse as sh"
		sed 's/^/     /' "$work/err"
		fail=$((fail + 1))
		continue
	fi

	# Everything before the first line that would change this machine.
	sed -E "/$stops/,\$d" "$block" > "$work/run.sh"
	if ! grep -q '[^[:space:]#]' "$work/run.sh"; then
		skip=$((skip + 1))
		continue
	fi
	if ! grep -qE 'curl|sha256sum|shasum|uname' "$work/run.sh"; then
		skip=$((skip + 1)) # nothing in it to get wrong
		continue
	fi

	if [ "$network" = no ]; then
		skip=$((skip + 1))
		continue
	fi

	dir="$work/run-$(basename "$block" .sh)"
	mkdir -p "$dir"
	if (cd "$dir" && sh -eu "$work/run.sh") >"$work/out" 2>&1; then
		echo "ok   $label"
		pass=$((pass + 1))
	else
		echo "FAIL $label"
		sed 's/^/     /' "$work/run.sh" | head -20
		echo "     ---"
		sed 's/^/     /' "$work/out" | tail -10
		fail=$((fail + 1))
	fi
done

echo "snippets: $pass ran, $skip nothing to run, $fail failed"
[ "$fail" = 0 ]
