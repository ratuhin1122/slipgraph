"""Prove NGINX upstream failures do not log OAuth codes or secret routing URIs.

Uses the built Compose web configuration with a closed loopback upstream. No real
credentials or existing application containers/volumes are touched.
"""
import secrets
import subprocess
import time
import urllib.error
import urllib.request


def docker(*args):
    result = subprocess.run(["docker", *args], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=45)
    return result.stdout.strip()


web = docker("compose", "ps", "-q", "web")
assert web and "\n" not in web, "start one Compose web container first"
image = docker("inspect", "--format", "{{.Config.Image}}", web)
assert image and "\n" not in image, "build the Compose web image first"
network = docker("network", "create", "slipgraph-github-check-" + secrets.token_hex(8))
container = None
try:
    container = docker("run", "-d", "--network", network, "-p", "127.0.0.1::8080", "--entrypoint", "/bin/sh", image, "-c",
                       "sed 's/server api:8080 resolve;/server 127.0.0.1:9;/' /etc/nginx/nginx.conf > /tmp/github-proxy-nginx.conf && exec nginx -c /tmp/github-proxy-nginx.conf -g 'daemon off;'")
    port = docker("port", container, "8080/tcp").split(":")[-1]
    origin = "http://127.0.0.1:" + port
    for attempt in range(30):
        try:
            with urllib.request.urlopen(origin + "/healthz", timeout=2) as response:
                assert response.status == 200
            break
        except (OSError, urllib.error.URLError):
            time.sleep(0.25)
    else:
        raise AssertionError("isolated proxy did not start")
    marker = "PRIVATE_URI_CANARY_" + secrets.token_hex(20)
    for path in ["/api/hooks/github/" + marker, "/api/integrations/github/callback?code=" + marker + "&state=" + marker]:
        try:
            urllib.request.urlopen(origin + path, timeout=15)
            raise AssertionError("isolated API unexpectedly resolved")
        except urllib.error.HTTPError as error:
            assert error.code in (502, 504), "expected an upstream failure"
    logs = subprocess.run(["docker", "logs", container], stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=15)
    assert marker.encode() not in logs.stdout + logs.stderr, "sensitive URI leaked through proxy error logging"
    print("GitHub proxy smoke passed: upstream failure logs omit OAuth and routing credentials.")
finally:
    if container:
        docker("rm", "-f", container)
    docker("network", "rm", network)
