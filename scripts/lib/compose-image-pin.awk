# Single source of truth for "does this Compose file pin the Foldex images?".
#
# Consumed by scripts/release.sh (read gate AND rewriter) and by
# scripts/validate-release-ref.sh (the publish gate inside release.yml). Those
# were three hand-copied awk blocks, and that is precisely how one wrong
# assumption reached all of them: the arity check `== 1` had to be written
# three times and was therefore wrong three times. One file cannot drift.
#
# Variables:
#   expected          the version every Foldex image line must currently carry
#   new               when non-empty, rewrite each Foldex line to this version
#                     and print the whole file; when empty, check and print
#                     nothing
#   require_services  when 1, the file is the PRIMARY compose: it must define
#                     services named `backend` and `web`, and each must run its
#                     own Foldex image. Secondary compose files pass 0 and are
#                     only required to pin whatever Foldex images they mention.
#
# Exit status: 0 when the file satisfies the rules, 1 otherwise, 2 on misuse.
# In rewrite mode a non-zero exit means the caller must discard the output —
# which is why every caller writes to a temporary and only `mv`s on success.
#
# Reasons are printed to stderr so the caller can keep a short headline and
# still tell the operator WHICH line is wrong.

function fail(reason) {
  print "  " reason > "/dev/stderr"
  bad = 1
}

BEGIN {
  if (expected == "") {
    print "compose-image-pin.awk: expected= is required" > "/dev/stderr"
    exit 2
  }
  rewrite = (new != "")
  expected_tag = "${FOLDEX_VERSION:-" expected "}"
  new_tag = "${FOLDEX_VERSION:-" new "}"
  want["backend"] = 1
  want["web"] = 1
}

# Top-level keys sit at column zero. `services:` opens the block and any other
# top-level key closes it — without this, `networks:` and `volumes:` entries
# would be read as services, since they are two-space keys just the same.
/^[^[:space:]#]/ {
  in_services = ($0 ~ /^services:[[:space:]]*$/)
  service = ""
  if (rewrite) print
  next
}

in_services && /^  [^[:space:]#][^:]*:[[:space:]]*$/ {
  service = $0
  sub(/^  /, "", service)
  sub(/:[[:space:]]*$/, "", service)
  if (rewrite) print
  next
}

{
  line = $0
  sub(/^[[:space:]]+/, "", line)

  # Anchored at the start of the left-trimmed line, so a commented-out example
  # is neither counted nor rewritten. Unanchored it would count, then fail to
  # equal the expected string, and refuse every release over a dead line.
  if (line !~ /^image:[[:space:]]*/) {
    if (rewrite) print
    next
  }

  value = line
  sub(/^image:[[:space:]]*/, "", value)
  comment = ""
  if (match(value, /[[:space:]]+#.*$/)) {
    comment = substr(value, RSTART)
    value = substr(value, 1, RSTART - 1)
  }
  sub(/[[:space:]]+$/, "", value)
  quote = ""
  if (value ~ /^".*"$/) quote = "\""
  else if (value ~ /^'.*'$/) quote = "'"
  if (quote != "") value = substr(value, 2, length(value) - 2)

  # Recorded for every wanted service, Foldex image or not. Counting lines says
  # nothing about WHICH service runs them, so a decoy service could carry the
  # pinned line while `backend:` ran anything at all. END decides; keeping the
  # value here is what lets it name the actual problem instead of guessing.
  if (service in want) svc_image[service] = value

  if (value !~ /justoeu\/foldex-/) {
    if (rewrite) print
    next
  }

  # Quoting and a registry prefix used to make a line invisible to the matcher:
  # it carried a Foldex image, was never verified, and passed in silence. The
  # canonical form is the only accepted one, so an unverifiable shape is a
  # refusal instead.
  if (value !~ /^justoeu\/foldex-[a-z0-9][a-z0-9._-]*:[^[:space:]]+$/) {
    fail("unrecognised Foldex image reference: " value)
    if (rewrite) print
    next
  }

  kind = value
  sub(/^justoeu\/foldex-/, "", kind)
  sub(/:.*$/, "", kind)
  tag = value
  sub(/^justoeu\/foldex-[^:]*:/, "", tag)

  count[kind]++
  if (service in want) svc_kind[service] = kind
  if (tag != expected_tag) {
    fail("image is not pinned to " expected ": " value)
  }

  if (rewrite) {
    indent = substr($0, 1, length($0) - length(line))
    print indent "image: " quote "justoeu/foldex-" kind ":" new_tag quote comment
  }
  next
}

END {
  if (require_services == 1) {
    # Ordered from most to least specific, so the reason names what is actually
    # wrong. A single "does not match" for all four cases sends the reader
    # looking at the version when the problem is a missing service.
    for (name in want) {
      if (!(name in svc_image)) {
        if (count[name] >= 1) {
          fail("no service named " name " (its image is on another service)")
        } else {
          fail("no service named " name)
        }
      } else if (!(name in svc_kind)) {
        fail("service " name " does not run a Foldex image: " svc_image[name])
      } else if (svc_kind[name] != name) {
        fail("service " name " runs justoeu/foldex-" svc_kind[name])
      }
    }
  }
  exit bad ? 1 : 0
}
