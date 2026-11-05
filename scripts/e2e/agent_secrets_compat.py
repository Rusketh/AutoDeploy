#!/usr/bin/env python3
"""A NEW agent against the CURRENT RELEASE server keeps working (legacy)."""
import json, os, sys, time
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import agent_secrets_upgrade as e

def main():
    os.makedirs(e.DATA, exist_ok=True)
    e.start_server("server-old"); e.stop("server")
    e.db_exec("INSERT INTO system_setting(key,value) VALUES('agent.poll_interval_seconds','5') ON CONFLICT(key) DO UPDATE SET value='5'")
    e.start_server("server-old"); e.login()
    code, body = e.admin("POST", "/api/v1/software", {"name": "App", "steps_json": json.dumps([{"type": "cmd", "script_body": "echo x"}]), "detection_json": "[]"})
    pkg = json.loads(body)["id"]
    e.admin("PUT", f"/api/v1/software/{pkg}/upload", raw=b"BYTES", headers={"Content-Type": "application/octet-stream", "X-Filename": "a.exe"})
    code, body = e.admin("POST", "/api/v1/images", {"name": "Img", "software_links": [{"package_id": pkg, "order_value": 1}]})
    img = json.loads(body)["id"]
    e.start_agent("newagent", "agent-new", "E2E-COMPAT-1", os.path.join(e.RUN, "m", "work"))
    mid = e.wait("enrol", lambda: (e.db("SELECT id FROM machine_record WHERE system_uuid='E2E-COMPAT-1'") or [[None]])[0][0])
    e.admin("PUT", f"/api/v1/machines/{mid}/binding", {"machine_id": mid, "image_id": img})
    e.check(e.wait("download", lambda: e.downloads("newagent") >= 1, 60) is not None, "new agent downloads software from the release server")
    j = e.queue_script_job(mid, "compat")
    e.check(e.wait("job", lambda: e.job_status(j) in ("ok", "failed")) and e.job_status(j) == "ok", "new agent's job result accepted by the release server")
    txt = e.logtext("newagent")
    e.check(txt.count('"/api/v1/agent/credential"') <= 1 and "agent.credential.obtained" not in txt, "new agent stops asking a server without secrets")
    for n in list(e.procs): e.stop(n)
    print("ALL CHECKS PASSED" if not e.failures else f"{len(e.failures)} failure(s)")
    sys.exit(1 if e.failures else 0)

if __name__ == "__main__":
    try: main()
    finally:
        for n in list(e.procs): e.stop(n)
