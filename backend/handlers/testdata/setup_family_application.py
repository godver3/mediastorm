"""Create synthetic accounts only on a loopback demo instance."""
import argparse
import ipaddress
import json
import os
import secrets
import stat
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

def require(condition, message):
    if not condition:
        raise ValueError(message)


def main(args):
    parsed = urllib.parse.urlsplit(args.base_url)
    require(parsed.scheme == "http" and not parsed.username and not parsed.password, "A credential-free local HTTP origin is required")
    require(parsed.path in ("", "/") and not parsed.query and not parsed.fragment, "A plain local origin is required")
    require(parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname or "").is_loopback, "A loopback server is required")
    base = args.base_url.rstrip("/")
    output = Path(args.output)
    require(not output.exists(), "Refuse to replace an existing credential fixture")
    source = Path(args.bootstrap_credentials_file)
    require(stat.S_IMODE(source.stat().st_mode) & 0o077 == 0, "Bootstrap credentials must be private")
    bootstrap = json.loads(source.read_text())

    def call(method, path, body=None, token=None):
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request(base + "/api" + path, method=method, headers=headers,
                                         data=json.dumps(body).encode() if body is not None else None)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"Synthetic setup request returned {error.code}") from None
        with response:
            raw = response.read()
            return json.loads(raw) if raw else None

    master = call("POST", "/auth/login", bootstrap)["token"]
    require(call("GET", "/settings", token=master)["demoMode"] is True, "The isolated server must run in demo mode")
    created, accounts = [], {}
    try:
        suffix = secrets.token_hex(5)
        for label in ("alpha", "beta"):
            username = "synthetic-family-" + label + "-" + suffix
            password = secrets.token_urlsafe(32)
            account = call("POST", "/accounts", {"username": username, "password": password}, master)
            created.append(account["id"])
            token = call("POST", "/auth/login", {"username": username, "password": password})["token"]
            profile = call("POST", "/users", {"name": "Synthetic " + label.title(), "password": password}, token)
            accounts[label] = {"username": username, "password": password, "token": token, "profileId": profile["id"]}
        alpha = accounts["alpha"]
        other = call("POST", "/users", {"name": "Synthetic No PIN", "password": alpha["password"]}, alpha["token"])
        pin = "".join(secrets.choice("0123456789") for _ in range(4))
        call("PUT", "/users/" + alpha["profileId"] + "/pin", {"pin": pin}, alpha["token"])
        value = {"fixture": "mediastorm-synthetic-family-v1", "username": alpha["username"],
                 "password": alpha["password"], "pin": pin, "profileId": alpha["profileId"],
                 "secondProfileId": other["id"], "foreignProfileId": accounts["beta"]["profileId"]}
        descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as target:
            json.dump(value, target)
    except Exception:
        for account in reversed(created):
            call("DELETE", "/accounts/" + account, token=master)
        raise
    print(json.dumps({"synthetic_fixture_created": True, "accounts_created": 2, "explicit_profiles_created": 3,
                      "default_account_profiles_preserved": True, "credentials_printed": False}))


if __name__ == "__main__":
    os.umask(0o077)
    parser = argparse.ArgumentParser()
    for argument in ("base-url", "bootstrap-credentials-file", "output"):
        parser.add_argument("--" + argument, required=True)
    main(parser.parse_args())
