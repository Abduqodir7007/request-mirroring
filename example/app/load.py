import json
import random
import time
import urllib.error
import urllib.request

BASE = "http://proxy:8019"


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method, headers={"Content-Type": "application/json"})
    try:
        urllib.request.urlopen(req, timeout=5).read()
    except urllib.error.HTTPError:
        pass
    except Exception as e:
        print("error", e, flush=True)
        time.sleep(1)


while True:
    r = random.random()
    if r < 0.5:
        call("GET", f"/api/items/{random.randint(1, 55)}")
    elif r < 0.75:
        call("GET", "/api/items")
    elif r < 0.9:
        call("POST", "/api/login", {"username": "demo", "password": "secret"})
    else:
        call("POST", "/api/login", {"username": "demo", "password": ""})
    time.sleep(0.1)
