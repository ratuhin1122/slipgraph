"""Check deployed admin integration boundaries without contacting GitHub."""
import urllib.parse
from smoke_client import request, mutate, fixture_identity

fixture_identity("GitHub boundary smoke", 0)
status = request("GET", "/integrations/github")
assert not status["connected"] and status["repository"] == ""
assert set(status) == {"enabled", "connected", "repository", "channel_id", "stale_days", "delay_days"}, "credentials exposed by status"
assert request("GET", "/integrations/github/events") == []
if status["enabled"]:
    authorization = mutate("POST", "/integrations/github/connect")
    parsed = urllib.parse.urlparse(authorization["url"])
    query = urllib.parse.parse_qs(parsed.query)
    assert parsed.scheme == "https" and parsed.netloc == "github.com"
    assert query["code_challenge_method"] == ["S256"] and "scope" not in query
else:
    mutate("POST", "/integrations/github/connect", expected=503)
assert request("POST", "/hooks/github/invalid", expected=404)["error"] == "not found"
mutate("DELETE", "/integrations/github", expected=204)
mutate("POST", "/auth/logout", expected=204)
print("GitHub deployed smoke passed: admin status, private logs, disabled/PKCE connect, JSON webhook routing and disconnect.")
