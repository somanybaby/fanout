"""Reject private runtime files, credentials and personal commit email addresses."""
import pathlib
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(["git", *args])


private_names = {
    "telegram.json", "password", "basepath", "xui-token", "state.json",
    "settings.json", "proxycheck-key", "proxycheck.json", "native-inbounds.json",
    "selections.json", "quality.json", "risk-cache.json", "verification.json",
    "validation-private.json", "id_rsa", "id_ed25519",
}
private_dirs = {".fanout-custom-validation", "update-backups", "diagnostic"}
credential = re.compile(
    rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"
    rb"|(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})"
)
errors = []
for raw in git("ls-files", "-z").split(b"\0"):
    if not raw:
        continue
    path = pathlib.Path(raw.decode("utf-8"))
    name = path.name
    if (
        name in private_names
        or (name.startswith(".env") and name != ".env.example")
        or private_dirs.intersection(path.parts)
        or path.suffix in {".bundle", ".pem", ".key", ".db", ".ovpn"}
    ):
        errors.append(f"Private runtime file is tracked: {path.as_posix()}")
    if path.is_file() and credential.search(path.read_bytes()):
        errors.append(f"Credential pattern found: {path.as_posix()}")

for line in git("log", "--all", "--format=%H%x09%ae%x09%ce").decode().splitlines():
    sha, author, committer = line.split("\t")
    for email in (author, committer):
        if not (email.endswith("@users.noreply.github.com") or email == "noreply@github.com"):
            errors.append(f"Personal commit email found in {sha[:12]}; use a GitHub noreply address")

if errors:
    print("\n".join(sorted(set(errors))))
    sys.exit(1)
print("Privacy checks passed: no private runtime files, key patterns or personal commit emails")
