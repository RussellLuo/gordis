#!/usr/bin/env python3
"""Build and exercise the process-backed application plugin example."""
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import time
import urllib.error
import urllib.request

EXAMPLE = Path(__file__).resolve().parent


def main():
    out = EXAMPLE / ".artifacts" / ("application-plugin-" + time.strftime("%Y%m%d-%H%M%S") + f"-{time.time_ns() % 1_000_000_000:09d}")
    out.mkdir(parents=True, exist_ok=False)
    checks = []
    host = None
    with (out / "commands.log").open("w") as commands, (out / "host.log").open("w") as host_log:
        def run(args):
            args = list(map(str, args))
            commands.write("$ " + " ".join(args) + "\n"); commands.flush()
            env = {**os.environ, "GOPROXY": "off", "GOWORK": str(EXAMPLE / "go.work")}
            result = subprocess.run(args, cwd=EXAMPLE, env=env, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, text=True, timeout=120)
            commands.write(result.stdout); commands.flush()
            if result.returncode:
                raise RuntimeError(result.stdout)

        def build(*flags):
            run(["node", EXAMPLE / "build.mjs", "--output=" + str(out), *flags])

        def poll(condition, timeout=8):
            end = time.monotonic() + timeout
            while time.monotonic() < end:
                value = condition()
                if value:
                    return value
                time.sleep(0.02)
            raise AssertionError("timed out")

        def request(path, body=None, generation=None, error=False, raw=False):
            headers = {"Content-Type": "application/json"}
            if generation is not None:
                headers["X-Gordis-Generation"] = str(generation)
            req = urllib.request.Request(url + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
            try:
                with urllib.request.urlopen(req, timeout=10) as response:
                    data = response.read()
            except urllib.error.HTTPError as exc:
                if not error:
                    raise
                data = exc.read()
            return data if raw else json.loads(data)

        def catalog():
            return request("api/plugins")

        def sample(name, generation):
            return request("api/extensions/sampler--" + name + "/sample", {"sequence": 7}, generation)

        def record(name):
            assert catalog()["hostPID"] == host_pid
            checks.append(name)
            print("PASS", name, flush=True)

        def host_hashes():
            files = [out / "host", *(out / "web").rglob("*")]
            return {str(p.relative_to(out)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files if p.is_file()}

        try:
            build("--host-only")
            original_hashes = host_hashes()
            ready = out / "ready"
            host = subprocess.Popen([str(out / "host"), "-addr", "127.0.0.1:0", "-ready-file", str(ready)],
                                    stdout=host_log, stderr=subprocess.STDOUT)
            poll(ready.exists)
            url = ready.read_text()
            first = catalog()
            host_pid = first["hostPID"]
            assert first["instances"] == first["packages"] == first["available"] == []
            record("host-starts-without-plugin")

            build("--packages-only", "--version=1.1.0")
            assert host_hashes() == original_hashes
            assert catalog()["available"][0]["versions"] == ["1.1.0"]
            request("api/package/load", {"packageID": "sampler", "version": "1.1.0"})
            current = catalog()
            assert {i["id"] for i in current["instances"]} == {"sampler--alpha", "sampler--beta"}
            assert len(current["packages"]) == 1 and all(
                i["mounted"] and i["phase"] == "ready" for i in current["instances"]
            )
            ui_v1 = current["packages"][0]
            assert b"overview" in request(ui_v1["entry"].removeprefix("/demo/"), raw=True)
            for style in ui_v1["styles"]:
                assert b"sampler-reading" in request(style.removeprefix("/demo/"), raw=True)
            alpha, beta = sample("alpha", 1), sample("beta", 1)
            assert alpha["value"] == beta["value"] == 1007 and alpha["pid"] != beta["pid"]
            record("late-package-loads-two-processes-and-one-UI")

            request("api/plugins/sampler--alpha/disable", {})
            assert len(catalog()["packages"]) == 1 and sample("beta", 1)["value"] == 1007
            request("api/plugins/sampler--beta/disable", {})
            assert catalog()["packages"] == []
            record("UI-follows-ready-instances")

            for name in ("alpha", "beta"):
                request("api/plugins/sampler--" + name + "/enable", {})
            assert sample("alpha", 2)["generation"] == 2
            assert catalog()["packages"][0]["entry"] == ui_v1["entry"]
            record("restart-increments-generation-without-rebuilding-UI")

            build("--packages-only", "--version=2.1.0")
            assert host_hashes() == original_hashes
            request("api/package/load", {"packageID": "sampler", "version": "2.1.0"})
            current = catalog()
            assert all(i["generation"] == 3 and i["version"] == "2.1.0" for i in current["instances"])
            assert current["packages"][0]["entry"] != ui_v1["entry"]
            assert sample("alpha", 3)["value"] == 2007
            assert "generation changed" in request("api/extensions/sampler--alpha/sample", {"sequence": 7}, 2, error=True)["error"]
            record("backend-and-UI-upgrade-without-host-restart")

            old = out / "packages/sampler/1.1.0"
            bundle = json.loads((old / "bundle.json").read_text())
            entry = old / bundle["ui"]["entry"]
            original = entry.read_bytes()
            try:
                entry.write_bytes(original + b"\n// changed\n")
                rejected = request("api/package/load", {"packageID": "sampler", "version": "1.1.0"}, error=True)
                assert "digest mismatch" in rejected["error"] and sample("alpha", 3)["value"] == 2007
            finally:
                entry.write_bytes(original)
            record("bad-package-does-not-replace-running-version")

            run(["node", EXAMPLE / "check-ui.mjs", out])
            request("api/package/uninstall", {"packageID": "sampler"})
            assert catalog()["instances"] == catalog()["packages"] == []
            processes = request("api/processes")
            assert processes and all(
                p["cleanupKnown"] and p["cleanup"]["complete"] and
                p["reaped"] and p["ioComplete"] and p["handlersComplete"] and
                p["pending"] == 0 and p["handling"] == 0
                for p in processes
            )
            record("uninstall-removes-UI-disposes-plugins-and-reaps-processes")
            (out / "summary.json").write_text(json.dumps({"result": "passed", "checks": checks,
                "hostPID": host_pid, "browserRendered": False}, indent=2) + "\n")
            print("Evidence:", out, flush=True)
        finally:
            if host is not None and host.poll() is None:
                host.send_signal(signal.SIGINT)
                try:
                    host.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    host.kill(); host.wait(timeout=5)


if __name__ == "__main__":
    main()
