#!/usr/bin/env python3
"""End-to-end test of agent secrets and the upgrade path, with real binaries.

Phases:
  A. An existing site: the CURRENT RELEASE server + agent (built from
     origin/main). Software installs and bulk jobs work.
  B. Server upgraded in place (same data dir; migrations run). The old agent
     keeps working unchanged (legacy authentication).
  C. Agent upgraded by its own self-update: the new agent is published in the
     server's downloads, the release agent (which has no secret) downloads
     it, swaps its binary and relaunches; the new agent obtains a secret on
     its own (first contact). From then on the old protocol is refused for
     that machine, and everything still works over the secured channel.
  D. Rotation: the server's rotation period is shortened; the agent rotates,
     the server confirms, and the agent is never refused.
  E. New channel at deploy: a new boot client's manifest carries the secret;
     an older boot client's doesn't (deploy grant instead).
  F. Takeover attempt: a second agent claiming the same machine is held for
     approval; only after an operator approves does it get a secret, and the
     original agent is then locked out (single holder).
  G. Approve all pending: the locked-out original asks again; one bulk
     approval in the portal hands it back its secret.
  H. Clones (legacy auth now OFF, as on a fully migrated site):
     - a disk copied with the agent's credential file onto other hardware
       detects the clone, enrols as a new machine and gets its own secret;
     - a golden image captured before secrets (agent id but no credential)
       finds out via whoami and does the same;
     - the server refuses the original's secret from foreign hardware (409);
     - the original machine is untouched throughout.
"""
import hashlib, http.cookiejar, json, os, re, shutil, signal, sqlite3, subprocess, sys, time, urllib.request, urllib.error

HERE = os.path.dirname(os.path.abspath(__file__))
# Binaries and scratch space come from run-agent-secrets.sh (or the env).
BIN = os.environ.get("E2E_BIN", os.path.join(HERE, "bin"))
RUN = os.environ.get("E2E_RUN", os.path.join(HERE, "run"))
DATA = os.path.join(RUN, "data")
PORT = 18090
BASE = f"http://127.0.0.1:{PORT}"
UUID = "E2E-UPGRADE-0001"
failures = []


def check(cond, msg):
    print(("ok:   " if cond else "FAIL: ") + msg, flush=True)
    if not cond:
        failures.append(msg)


def db(q, args=()):
    c = sqlite3.connect(os.path.join(DATA, "autodeploy.sqlite"), timeout=30)
    try:
        return c.execute(q, args).fetchall()
    finally:
        c.close()


def db_exec(q, args=()):
    c = sqlite3.connect(os.path.join(DATA, "autodeploy.sqlite"), timeout=30)
    try:
        c.execute(q, args)
        c.commit()
    finally:
        c.close()


procs = {}


def start(name, argv, env=None):
    log = open(os.path.join(RUN, name + ".log"), "ab")
    e = dict(os.environ)
    e.update(env or {})
    procs[name] = subprocess.Popen(argv, stdout=log, stderr=subprocess.STDOUT, env=e)


def stop(name):
    p = procs.pop(name, None)
    if p:
        p.send_signal(signal.SIGTERM)
        try:
            p.wait(10)
        except subprocess.TimeoutExpired:
            p.kill()


def logtext(name):
    with open(os.path.join(RUN, name + ".log"), "rb") as f:
        return f.read().decode("utf-8", "replace")


def start_server(binary, extra=None):
    env = {"AUTODEPLOY_DATA_DIR": DATA, "AUTODEPLOY_HTTP_ADDR": f"127.0.0.1:{PORT}"}
    env.update(extra or {})
    start("server", [os.path.join(BIN, binary)], env)
    for _ in range(100):
        try:
            urllib.request.urlopen(BASE + "/portal/login", timeout=2)
            return
        except Exception:
            time.sleep(0.2)
    raise SystemExit("server didn't start")


def start_agent(name, binary, uuid, work, self_update=False, extra=()):
    exe = binary if os.path.isabs(binary) else os.path.join(BIN, binary)
    argv = [exe, "--server", BASE, "--uuid", uuid, "--work", work, "--check-in", "5s", "--dry-run"]
    if not self_update:
        argv.append("--no-self-update")
    start(name, argv + list(extra))


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def pids_running(exe):
    """PIDs of agent processes running exe (not the updater script)."""
    out = subprocess.run(["pgrep", "-f", "^" + re.escape(exe) + " --server"], capture_output=True, text=True).stdout
    return [int(p) for p in out.split()]


SHOTS = os.environ.get("E2E_SHOTS", "")


def shot(name, path, selector):
    """Screenshot an element of a portal page into $E2E_SHOTS (optional)."""
    if not SHOTS:
        return
    cookies = json.dumps([{"name": c.name, "value": c.value} for c in jar])
    r = subprocess.run(["node", os.path.join(HERE, "shot.js"), BASE, cookies, path, selector,
                        os.path.join(SHOTS, name)], capture_output=True, text=True)
    check(r.returncode == 0, f"screenshot {name}" + (": " + r.stderr.strip() if r.returncode else ""))


def portal_post(path):
    return admin("POST", path, raw=b"", headers={"Content-Type": "application/x-www-form-urlencoded"})


jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def call(method, path, body=None, headers=None, raw=None, op=None):
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(BASE + path, data=data, method=method)
    req.add_header("Content-Type", "application/json")
    req.add_header("X-Requested-With", "e2e")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    try:
        r = (op or urllib.request.urlopen)(req, timeout=20) if op is None else op.open(req, timeout=20)
        return r.status, r.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode()


def admin(method, path, body=None, raw=None, headers=None):
    return call(method, path, body, headers=headers, raw=raw, op=opener)


def login():
    pw = open(os.path.join(DATA, "admin-bootstrap.txt")).read().split("password: ")[1].split()[0]
    code, _ = admin("POST", "/api/v1/auth/login", {"username": "admin", "password": pw})
    assert code == 200, code


def wait(desc, fn, timeout=60):
    end = time.time() + timeout
    while time.time() < end:
        v = fn()
        if v:
            return v
        time.sleep(1)
    check(False, "timed out waiting for " + desc)
    return None


def queue_script_job(mid, tag):
    code, body = admin("POST", "/api/v1/bulk/operations", {
        "action": "script", "payload": json.dumps({"shell": "cmd", "body": "echo " + tag}),
        "target": {"machine_ids": [mid]}})
    assert code == 201, (code, body)
    return json.loads(body)["jobs"][0]["id"]


def job_status(jid):
    rows = db("SELECT status FROM bulk_job WHERE id=?", (jid,))
    return rows[0][0] if rows else None


def downloads(name):
    return logtext(name).count("package.download.ok")


def main():
    os.makedirs(DATA, exist_ok=True)
    work1 = os.path.join(RUN, "machine1", "work")

    # ---- A. existing site: current release server + agent ----------------
    print("== A. existing site (release server + release agent)")
    start_server("server-old")
    stop("server")  # first start creates the DB; set a 5s check-in and restart
    db_exec("INSERT INTO system_setting(key,value) VALUES('agent.poll_interval_seconds','5') "
            "ON CONFLICT(key) DO UPDATE SET value='5'")
    start_server("server-old")
    login()
    code, body = admin("POST", "/api/v1/software", {
        "name": "E2E App", "steps_json": json.dumps([{"type": "cmd", "script_body": "echo installed"}]),
        "detection_json": "[]"})
    assert code in (200, 201), (code, body)
    pkg = json.loads(body)["id"]
    code, body = admin("PUT", f"/api/v1/software/{pkg}/upload", raw=b"PAYLOAD-BYTES",
                       headers={"Content-Type": "application/octet-stream", "X-Filename": "app.exe"})
    assert code == 200, (code, body)
    code, body = admin("POST", "/api/v1/images", {"name": "E2E Image", "software_links": [{"package_id": pkg, "order_value": 1}]})
    assert code in (200, 201), (code, body)
    img = json.loads(body)["id"]

    # The machine's installed agent: a copy of the release binary, run with
    # self-update ON (as deployed agents run), so phase C can upgrade it.
    exe1 = os.path.join(RUN, "machine1", "bin", "autodeploy-agent")
    os.makedirs(os.path.dirname(exe1), exist_ok=True)
    shutil.copy2(os.path.join(BIN, "agent-old"), exe1)
    start_agent("agent1", exe1, UUID, work1, self_update=True)
    mid = wait("machine enrolment", lambda: (db("SELECT id FROM machine_record WHERE system_uuid=?", (UUID,)) or [[None]])[0][0])
    agent_id = db("SELECT agent_id FROM machine_record WHERE id=?", (mid,))[0][0]
    code, _ = admin("PUT", f"/api/v1/machines/{mid}/binding", {"machine_id": mid, "image_id": img})
    check(code in (200, 204), "machine bound to the image")
    check(wait("release agent downloads software", lambda: downloads("agent1") >= 1, 60) is not None,
          "A: release agent downloads its image's software")
    j = queue_script_job(mid, "phase-a")
    check(wait("job A", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok", "A: release agent runs a bulk job")

    # ---- B. server upgraded in place --------------------------------------
    print("== B. server upgraded, release agent unchanged")
    stop("server")
    start_server("server-new")
    login()
    check(db("SELECT COUNT(*) FROM agent_credential WHERE machine_id=?", (mid,))[0][0] == 0,
          "B: machine has no secret yet (legacy)")
    before = downloads("agent1")
    check(wait("legacy download", lambda: downloads("agent1") > before, 60) is not None,
          "B: release agent still downloads software from the upgraded server")
    j = queue_script_job(mid, "phase-b")
    check(wait("job B", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "B: release agent still runs bulk jobs (result accepted by UUID)")
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id)
    check(code == 200, "B: legacy /self still accepted for a legacy machine")

    # ---- C. agent upgraded -------------------------------------------------
    print("== C. agent upgraded by self-update (new agent published on the server)")
    old_ver = subprocess.run([exe1, "--version"], capture_output=True, text=True).stdout.strip()
    new_ver = subprocess.run([os.path.join(BIN, "agent-new"), "--version"], capture_output=True, text=True).stdout.strip()
    dl = os.path.join(DATA, "downloads")
    os.makedirs(dl, exist_ok=True)
    shutil.copy2(os.path.join(BIN, "agent-new"), os.path.join(dl, "autodeploy-agent-linux-amd64"))
    with open(os.path.join(dl, "autodeploy-agent-linux-amd64.version"), "w") as f:
        f.write(new_ver + "\n")
    code, body = call("POST", "/api/v1/agent/update-info", {"os": "linux", "arch": "amd64", "current_version": old_ver})
    check(code == 200 and json.loads(body).get("update_available"),
          f"C: server offers {new_ver} to the {old_ver} agent (update-info needs no secret)")
    new_sha = sha256(os.path.join(BIN, "agent-new"))
    # The release agent exits so its updater can swap the binary. Reap it
    # as a service manager would (the updater waits for the PID to go; an
    # unreaped zombie would look alive to it).
    try:
        procs["agent1"].wait(120)
        check(True, "C: release agent process exited for the swap")
    except subprocess.TimeoutExpired:
        check(False, "C: release agent process exited for the swap")
    procs.pop("agent1", None)
    check("selfupdate.downloaded" in logtext("agent1"), "C: release agent logged the verified download")
    check(bool(wait("self-update swaps the binary", lambda: os.path.exists(exe1) and sha256(exe1) == new_sha, 60)),
          "C: updater swapped in the new binary and relaunched the agent")
    row = wait("secret issued", lambda: db("SELECT secret_hash, issued_via FROM agent_credential WHERE machine_id=? AND secret_hash!=''", (mid,)), 90)
    check(bool(row) and row[0][1] == "first-contact", "C: self-updated agent obtained a secret on first contact")
    cred_file = os.path.join(RUN, "machine1", "identity", "credential.json")
    check(bool(wait("credential file", lambda: os.path.exists(cred_file) and json.load(open(cred_file)).get("secret"), 30)),
          "C: self-updated agent stored its secret")
    check(subprocess.run([exe1, "--version"], capture_output=True, text=True).stdout.strip() == new_ver,
          f"C: installed agent now reports {new_ver}")
    # The relaunched agent logs to /dev/null; restart it under the harness
    # (same binary, same machine) so the rest of the run can read its log.
    for pid in pids_running(exe1):
        os.kill(pid, signal.SIGTERM)
    wait("relaunched agent stops", lambda: not pids_running(exe1), 20)
    start_agent("agent1new", exe1, UUID, work1)
    check(os.path.exists(cred_file) and (os.stat(cred_file).st_mode & 0o777) == 0o600,
          "C: secret stored in the agent's protected credential file (0600)")
    stored = json.load(open(cred_file))
    check(stored["agent_id"] == agent_id and len(stored["secret"]) >= 43, "C: stored credential is this machine's")
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id)
    check(code == 401, "C: old protocol (agent_id only) now refused for this machine")
    code, _ = call("POST", "/api/v1/agent/checkin", {"identity": {"system_uuid": UUID}})
    check(code == 401, "C: UUID-only check-in (job claiming) now refused for this machine")
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id,
                   headers={"X-AutoDeploy-Agent-ID": agent_id, "X-AutoDeploy-Agent-Secret": stored["secret"]})
    check(code == 200, "C: the stored secret opens the channel")
    before = downloads("agent1new")
    check(wait("secured download", lambda: downloads("agent1new") > before, 60) is not None,
          "C: upgraded agent downloads software over the secured channel")
    j = queue_script_job(mid, "phase-c")
    check(wait("job C", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "C: upgraded agent runs bulk jobs over the secured channel")
    check("agent credential required" not in logtext("agent1new"), "C: upgraded agent was never refused")
    check(stored["secret"] not in logtext("agent1new") and stored["secret"] not in logtext("server"),
          "C: the secret appears in no agent or server log")

    # ---- D. rotation --------------------------------------------------------
    print("== D. rotation (period shortened to 1m)")
    stop("server")
    start_server("server-new", {"AUTODEPLOY_AGENT_SECRET_ROTATION": "1m"})
    login()
    h0 = db("SELECT secret_hash FROM agent_credential WHERE machine_id=?", (mid,))[0][0]
    r = wait("rotation", lambda: db("SELECT secret_hash, prev_secret_hash FROM agent_credential WHERE machine_id=? AND secret_hash!=? AND prev_secret_hash=''", (mid, h0)), 120)
    check(bool(r), "D: agent rotated its secret and the server confirmed it (previous cleared)")
    s2 = json.load(open(cred_file))
    check(s2["secret"] != stored["secret"] and not s2.get("pending_secret"), "D: agent committed the new secret")
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id,
                   headers={"X-AutoDeploy-Agent-ID": agent_id, "X-AutoDeploy-Agent-Secret": stored["secret"]})
    check(code == 401, "D: the old secret no longer works")
    j = queue_script_job(mid, "phase-d")
    check(wait("job D", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "D: agent keeps working after rotation")
    check("agent credential required" not in logtext("agent1new"), "D: agent was never refused across rotation")
    check("agent.credential.rotated" in logtext("agent1new"), "D: agent logged the rotation")

    # ---- E. deploy-time issuance --------------------------------------------
    print("== E. secret issued at deploy (boot client manifest)")
    code, body = call("POST", f"/api/v1/images/{img}/manifest", {"system_uuid": "E2E-PXE-NEW", "want_credential": True})
    man = json.loads(body)
    check(code == 200 and man.get("agent_secret") and man.get("agent_id"), "E: new boot client receives the agent secret in the manifest")
    code, _ = call("GET", "/api/v1/agent/self?id=" + man["agent_id"],
                   headers={"X-AutoDeploy-Agent-ID": man["agent_id"], "X-AutoDeploy-Agent-Secret": man["agent_secret"]})
    check(code == 200, "E: the deploy-issued secret works")
    code, _ = call("GET", "/api/v1/agent/self?id=" + man["agent_id"])
    check(code == 401, "E: a deployed machine is never reachable without its secret")
    code, body = call("POST", f"/api/v1/images/{img}/manifest", {"system_uuid": "E2E-PXE-OLD"})
    old = json.loads(body)
    oid = db("SELECT id FROM machine_record WHERE system_uuid='E2E-PXE-OLD'")[0][0]
    check(not old.get("agent_secret") and db("SELECT COUNT(*) FROM agent_deploy_grant WHERE machine_id=?", (oid,))[0][0] == 1,
          "E: older boot client gets no secret, but a one-time deploy grant")

    # ---- F. takeover attempt + approval -------------------------------------
    print("== F. a second agent claims the same machine")
    work2 = os.path.join(RUN, "intruder", "work")
    start_agent("intruder", "agent-new", UUID, work2)
    req = wait("pending request", lambda: db("SELECT id FROM agent_credential_request WHERE machine_id=? AND status='pending'", (mid,)), 60)
    check(bool(req), "F: second claimant is held for approval (no secret issued)")
    time.sleep(6)
    check(not os.path.exists(os.path.join(RUN, "intruder", "identity", "credential.json")) or
          not json.load(open(os.path.join(RUN, "intruder", "identity", "credential.json"))).get("secret"),
          "F: second claimant holds no secret while pending")
    j = queue_script_job(mid, "phase-f")
    check(wait("job F", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "F: the real agent is unaffected while the request is pending")
    # Operator approves it in the portal.
    code, _ = admin("POST", f"/portal/machines/{mid}/credential/requests/{req[0][0]}/approve", raw=b"",
                    headers={"Content-Type": "application/x-www-form-urlencoded"})
    check(code in (200, 302), "F: operator approves the request in the portal")
    check(bool(wait("approved claimant obtains secret", lambda: "agent.credential.obtained" in logtext("intruder"), 60)),
          "F: the approved requester obtains a secret")
    check(bool(wait("original agent locked out and re-requests",
                    lambda: db("SELECT COUNT(*) FROM agent_credential_request WHERE machine_id=? AND status='pending'", (mid,))[0][0] >= 1, 60)),
          "F: the original agent's secret stops working; it asks for approval again (single holder)")

    # ---- G. approve all pending ------------------------------------------
    print("== G. approve all pending (portal bulk approval)")
    stop("intruder")
    obtained = logtext("agent1new").count("agent.credential.obtained")
    pend = db("SELECT COUNT(*) FROM agent_credential_request WHERE status='pending'")[0][0]
    code, body = admin("GET", "/portal/machines")
    check(code == 200 and "Approve all pending" in body, "G: machine list offers Approve all pending")
    shot("machines-approve-all.png", "/portal/machines", "#credential-pending-banner")
    shot("machine-credential-pending.png", f"/portal/machines/{mid}", "#agent-credential-panel")
    code, _ = portal_post("/portal/machines/credential/approve-all")
    check(code in (200, 302, 303), f"G: operator approves all pending requests ({pend})")
    check(db("SELECT COUNT(*) FROM agent_credential_request WHERE status='pending'")[0][0] == 0,
          "G: no requests left pending")
    check(bool(wait("original agent obtains secret again",
                    lambda: logtext("agent1new").count("agent.credential.obtained") > obtained, 60)),
          "G: the original agent obtains its secret again")
    j = queue_script_job(mid, "phase-g")
    check(wait("job G", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "G: original agent works over the secured channel again")
    shot("machine-credential-secured.png", f"/portal/machines/{mid}", "#agent-credential-panel")

    # ---- H. clones ----------------------------------------------------------
    print("== H. clones (legacy agent authentication OFF)")
    stop("server")
    start_server("server-new", {"AUTODEPLOY_AGENT_LEGACY_AUTH": "false"})
    login()
    orig = json.load(open(cred_file))

    # H1. Disk copied with the credential file, booted on other hardware.
    c1 = os.path.join(RUN, "clone1")
    shutil.copytree(os.path.join(RUN, "machine1"), c1, ignore=shutil.ignore_patterns("bin"))
    start_agent("clone1", "agent-new", "E2E-CLONE-0001", os.path.join(c1, "work"), extra=["--agent-id", agent_id])
    cm1 = wait("clone 1 enrols as itself", lambda: (db("SELECT id FROM machine_record WHERE system_uuid='E2E-CLONE-0001'") or [[None]])[0][0], 60)
    check(bool(cm1) and cm1 != mid, "H1: copied-disk clone enrolled as a new machine")
    check("agent.clone.detected" in logtext("clone1"), "H1: clone detected from the copied credential (hardware differs)")
    r = wait("clone 1 secret", lambda: db("SELECT issued_via FROM agent_credential WHERE machine_id=? AND secret_hash!=''", (cm1,)), 60) if cm1 else None
    check(bool(r) and r[0][0] == "new-machine", "H1: clone got its own secret automatically (new machine)")
    cc1 = wait("clone 1 credential", lambda: os.path.exists(os.path.join(c1, "identity", "credential.json")) and
               json.load(open(os.path.join(c1, "identity", "credential.json"))), 30) or {}
    check(cc1.get("agent_id") not in ("", None, agent_id) and cc1.get("secret") != orig["secret"],
          "H1: clone's credential file now holds its own identity, not the original's")

    # H2. Golden image captured before secrets: agent id, no credential file.
    c2 = os.path.join(RUN, "clone2")
    start_agent("clone2", "agent-new", "E2E-CLONE-0002", os.path.join(c2, "work"), extra=["--agent-id", agent_id])
    cm2 = wait("clone 2 enrols as itself", lambda: (db("SELECT id FROM machine_record WHERE system_uuid='E2E-CLONE-0002'") or [[None]])[0][0], 60)
    check(bool(cm2) and cm2 != mid, "H2: golden-image clone (no credential) found out via whoami and enrolled as itself")
    r = wait("clone 2 secret", lambda: db("SELECT issued_via FROM agent_credential WHERE machine_id=? AND secret_hash!=''", (cm2,)), 60) if cm2 else None
    check(bool(r) and r[0][0] == "new-machine", "H2: golden-image clone got its own secret automatically")

    # H3. Server backstop: the original's secret from foreign hardware.
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id, headers={
        "X-AutoDeploy-Agent-ID": agent_id, "X-AutoDeploy-Agent-Secret": json.load(open(cred_file))["secret"],
        "X-AutoDeploy-Machine-UUID": "E2E-SOMEWHERE-ELSE"})
    check(code == 409, "H3: server refuses the original's secret presented from other hardware (409 cloned)")
    code, _ = call("GET", "/api/v1/agent/whoami?id=" + agent_id, headers={"X-AutoDeploy-Machine-UUID": UUID.lower()})
    check(code == 200, "H3: whoami answers without a secret")

    # The original is untouched.
    cur = json.load(open(cred_file))
    check(cur["agent_id"] == agent_id, "H: original keeps its agent id")
    code, _ = call("GET", "/api/v1/agent/self?id=" + agent_id, headers={
        "X-AutoDeploy-Agent-ID": agent_id, "X-AutoDeploy-Agent-Secret": cur["secret"], "X-AutoDeploy-Machine-UUID": UUID.lower()})
    check(code == 200, "H: original's own secret still works from its own hardware")
    check(db("SELECT COUNT(*) FROM agent_credential_request WHERE machine_id=? AND status='pending'", (mid,))[0][0] == 0,
          "H: clones raised no approval request against the original")
    j = queue_script_job(mid, "phase-h")
    check(wait("job H", lambda: job_status(j) in ("ok", "failed")) and job_status(j) == "ok",
          "H: original agent still works")

    # ---- I. media enrolment card -------------------------------------------
    print("== I. media enrolment (USB/ISO) on the image page")
    # Exporting an ISO needs Windows media; stand in for the export by
    # recording the token it would create (token issuance is unit-tested).
    db_exec("INSERT INTO image_enroll_token(image_id, token_hash, expires_at) VALUES(?, 'e2e', '9999-12-31 00:00:00')", (img,))
    code, body = admin("GET", f"/portal/images/{img}/resolved")
    check(code == 200 and "Revoke media enrolment" in body and "Nothing expires" in body,
          "I: image page shows the non-expiring media enrolment with a revoke button")
    shot("image-media-enrolment.png", f"/portal/images/{img}/resolved", "#media-enrolment")
    code, _ = portal_post(f"/portal/images/{img}/media-enrolment/revoke")
    check(code in (200, 302, 303) and db("SELECT COUNT(*) FROM image_enroll_token WHERE image_id=?", (img,))[0][0] == 0,
          "I: operator revokes the image's media enrolment")

    for n in list(procs):
        stop(n)
    print()
    print(f"{len(failures)} failure(s)" if failures else "ALL CHECKS PASSED")
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    try:
        main()
    finally:
        for n in list(procs):
            stop(n)
