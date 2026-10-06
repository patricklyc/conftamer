"""Regression tests for scripts/setup-go.sh and bin/ctgo.

Run from contexttrack/ with:  python3 -m unittest tests/test_tooling.py -v
Requires a stock Go 1.26.6 (CONFTAMER_GO or `go` on PATH); without one the
tests are skipped, or fail if CONTEXTTRACK_REQUIRE_GO=1. The clone-mode test
copies the whole GOROOT and runs only with CONTEXTTRACK_TEST_CLONE=1.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

CT = Path(__file__).resolve().parents[1]
SETUP = CT / "scripts" / "setup-go.sh"
CTGO = CT / "bin" / "ctgo"
ADDED = CT / "_goroot" / "src" / "net" / "http" / "conftamer.go"
TARGETS = ["client.go", "h2_bundle.go", "request.go", "server.go", "transport.go"]
REQUIRED = "go1.26.6"


def _real_go():
    go = shutil.which(os.environ.get("CONFTAMER_GO", "go"))
    if not go:
        return None, None
    env = {k: v for k, v in os.environ.items() if k not in ("GOFLAGS", "GOROOT")}
    env["GOTOOLCHAIN"] = "local"
    out = subprocess.run([go, "env", "GOROOT", "GOVERSION"], env=env, cwd="/",
                         capture_output=True, text=True)
    if out.returncode:
        return None, None
    goroot, version = out.stdout.split("\n")[:2]
    if version.split(" ")[0] != REQUIRED:
        return None, None
    if (Path(goroot) / "src/net/http/conftamer.go").exists():
        return None, None  # already patched: not a stock toolchain
    return go, Path(goroot)


GO, GOROOT = _real_go()
if not GO and os.environ.get("CONTEXTTRACK_REQUIRE_GO") == "1":
    raise SystemExit(f"test_tooling: no stock {REQUIRED} toolchain (CONFTAMER_GO or go)")

# Forwards to the real go, except: reports SHIM_GOROOT/SHIM_GOVERSION to
# `go env GOROOT GOVERSION`, and records `test`/`run` instead of running them.
SHIM = textwrap.dedent("""\
    #!{python}
    import json, os, sys
    real = {go!r}
    args = sys.argv[1:]
    if args == ["env", "GOROOT", "GOVERSION"] and (
            os.environ.get("SHIM_GOROOT") or os.environ.get("SHIM_GOVERSION")):
        print(os.environ.get("SHIM_GOROOT") or {goroot!r})
        print(os.environ.get("SHIM_GOVERSION") or {version!r})
        sys.exit(0)
    if args and args[0] in ("-C", "test", "run") and os.environ.get("SHIM_LOG"):
        keys = ("CONFTAMER_EVENTS", "GOFLAGS", "GOTOOLCHAIN")
        with open(os.environ["SHIM_LOG"], "w") as f:
            json.dump({{"args": args, "env": {{k: os.environ.get(k) for k in keys}}}}, f)
        sys.exit(0)
    os.execv(real, [real] + args)
    """)


@unittest.skipUnless(GO, f"needs a stock {REQUIRED} toolchain (CONFTAMER_GO or go)")
class ToolingTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="contexttrack-tooling-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.cache = self.tmp / "cache"
        self.events_dir = self.tmp / "events"
        self.env = {k: v for k, v in os.environ.items()
                    if k not in ("GOFLAGS", "GOROOT", "CONFTAMER_EVENTS", "CONFTAMER_GO")}
        self.env.update(CONFTAMER_CACHE_DIR=str(self.cache),
                        CONFTAMER_EVENTS_DIR=str(self.events_dir),
                        CONFTAMER_GO=GO)
        self.shim = self.tmp / "shim-go"
        self.shim.write_text(SHIM.format(python=sys.executable, go=GO,
                                         goroot=str(GOROOT), version=REQUIRED))
        self.shim.chmod(0o755)

    def run_cmd(self, cmd, check=True, cwd=None, **env):
        out = subprocess.run([str(c) for c in cmd], env={**self.env, **env},
                             cwd=cwd or self.tmp, capture_output=True, text=True)
        if check and out.returncode:
            self.fail(f"{cmd} failed ({out.returncode}):\n{out.stderr}")
        return out

    def setup_overlay(self):
        return Path(self.run_cmd([SETUP, "-q"]).stdout.strip())

    def cache_entries(self):
        return sorted(p.name for p in self.cache.iterdir()) if self.cache.exists() else []

    def fake_goroot(self):
        root = self.tmp / "fake-goroot"
        (root / "src/net/http").mkdir(parents=True)
        for name in TARGETS:
            shutil.copy(GOROOT / "src/net/http" / name, root / "src/net/http" / name)
        return root

    # setup-go.sh

    def test_overlay_is_built_once_and_leaves_goroot_stock(self):
        overlay = self.setup_overlay()
        manifest = overlay.parent / "manifest"
        mtime = manifest.stat().st_mtime_ns
        self.assertEqual(self.setup_overlay(), overlay)
        self.assertEqual(manifest.stat().st_mtime_ns, mtime, "cached overlay was rebuilt")
        self.assertEqual(self.cache_entries(), [overlay.parent.name])

        replace = json.loads(overlay.read_text())["Replace"]
        stock = GOROOT / "src/net/http"
        self.assertEqual(set(replace), {str(stock / n) for n in TARGETS + ["conftamer.go"]})
        for name in TARGETS:
            self.assertNotEqual(Path(replace[str(stock / name)]).read_bytes(),
                                (stock / name).read_bytes(), f"{name} was not patched")
        self.assertEqual(Path(replace[str(stock / "conftamer.go")]).read_bytes(),
                         ADDED.read_bytes())
        self.assertFalse((stock / "conftamer.go").exists())

    def test_rejects_other_go_version(self):
        out = self.run_cmd([SETUP, "--go", self.shim], check=False, SHIM_GOVERSION="go1.25.0")
        self.assertNotEqual(out.returncode, 0)
        self.assertIn(f"targets {REQUIRED}", out.stderr)
        self.assertEqual(self.cache_entries(), [])

    def test_rejects_already_patched_goroot(self):
        root = self.fake_goroot()
        shutil.copy(ADDED, root / "src/net/http/conftamer.go")
        out = self.run_cmd([SETUP, "--go", self.shim], check=False, SHIM_GOROOT=str(root))
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("already exists", out.stderr)
        self.assertEqual(self.cache_entries(), [])

    def test_rejects_unsafe_cache_paths(self):
        for name in (
            "cache space",
            "cache\ttab",
            "cache\nline",
            'cache"quote',
            "cache\\backslash",
        ):
            with self.subTest(path=name):
                cache = self.tmp / name
                out = self.run_cmd(
                    [SETUP, "-q"], check=False, CONFTAMER_CACHE_DIR=str(cache)
                )
                self.assertNotEqual(out.returncode, 0)
                self.assertIn(
                    "paths must not contain whitespace, quotes, or backslashes",
                    out.stderr,
                )
                self.assertFalse(
                    cache.exists(), "rejected path installed a cache entry"
                )
                self.assertEqual(self.cache_entries(), [])

    def test_patch_mismatch_installs_nothing(self):
        root = self.fake_goroot()
        (root / "src/net/http/client.go").write_text("package http\n")
        out = self.run_cmd([SETUP, "--go", self.shim], check=False, SHIM_GOROOT=str(root))
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("does not apply cleanly", out.stderr)
        self.assertEqual(self.cache_entries(), [], "failed build left cache entries")
        self.assertEqual((root / "src/net/http/client.go").read_text(), "package http\n")

    def test_clone_refuses_existing_destination(self):
        dest = self.tmp / "exists"
        dest.mkdir()
        out = self.run_cmd([SETUP, "--clone", dest], check=False)
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("already exists", out.stderr)
        self.assertEqual(list(dest.iterdir()), [])

    @unittest.skipUnless(os.environ.get("CONTEXTTRACK_TEST_CLONE") == "1",
                         "set CONTEXTTRACK_TEST_CLONE=1 to copy GOROOT")
    def test_clone_mode(self):
        dest = self.tmp / "go-conftamer"
        go = Path(self.run_cmd([SETUP, "-q", "--clone", dest]).stdout.strip())
        self.assertEqual(go, dest / "bin/go")
        env = {k: v for k, v in self.env.items() if k != "GOROOT"}
        env["GOTOOLCHAIN"] = "local"
        out = subprocess.run([go, "list", "-f", "{{.GoFiles}}", "net/http"], env=env,
                             capture_output=True, text=True, check=True)
        self.assertIn("conftamer.go", out.stdout)
        self.assertFalse((GOROOT / "src/net/http/conftamer.go").exists())

    # ctgo

    def shim_invocation(self, *args, **env):
        log = self.tmp / "shim.json"
        self.run_cmd([CTGO, *args], CONFTAMER_GO=str(self.shim), SHIM_LOG=str(log), **env)
        return json.loads(log.read_text())

    def test_ctgo_test_adds_count_and_fresh_capture(self):
        got = self.shim_invocation("test", "./x")
        self.assertEqual(got["args"], ["test", "-count=1", "./x"])
        events = Path(got["env"]["CONFTAMER_EVENTS"])
        self.assertEqual(events.parent, self.events_dir)
        self.assertTrue(events.exists())
        self.assertEqual(got["env"]["GOTOOLCHAIN"], "local")
        self.assertEqual(got["env"]["GOFLAGS"], f"-overlay={self.setup_overlay()}")
        self.assertNotEqual(self.shim_invocation("test")["env"]["CONFTAMER_EVENTS"], str(events))

    def test_ctgo_keeps_explicit_count_and_events(self):
        got = self.shim_invocation("-C", "d", "test", "-count=3", ".", CONFTAMER_EVENTS="")
        self.assertEqual(got["args"], ["-C", "d", "test", "-count=3", "."])
        self.assertEqual(got["env"]["CONFTAMER_EVENTS"], "")
        got = self.shim_invocation("-C", "d", "test", ".", CONFTAMER_EVENTS="/x.jsonl")
        self.assertEqual(got["args"], ["-C", "d", "test", "-count=1", "."])
        self.assertEqual(got["env"]["CONFTAMER_EVENTS"], "/x.jsonl")
        self.assertEqual(self.shim_invocation("run", ".")["args"], ["run", "."])

    def test_ctgo_goflags(self):
        overlay = self.setup_overlay()
        got = self.shim_invocation("test", GOFLAGS="-mod=mod")
        self.assertEqual(got["env"]["GOFLAGS"], f"-mod=mod -overlay={overlay}")
        got = self.shim_invocation("test", GOFLAGS=f"-mod=mod -overlay={overlay}")
        self.assertEqual(got["env"]["GOFLAGS"], f"-mod=mod -overlay={overlay}")
        out = self.run_cmd([CTGO, "version"], check=False, GOFLAGS="-overlay=/other.json")
        self.assertNotEqual(out.returncode, 0)
        self.assertIn("different overlay", out.stderr)

    def test_ctgo_run_emits_events(self):
        prog = self.tmp / "prog"
        prog.mkdir()
        (prog / "go.mod").write_text("module prog\n\ngo 1.26\n")
        (prog / "main.go").write_text(textwrap.dedent("""\
            package main

            import (
            \t"net/http"
            \t"net/http/httptest"
            )

            func main() {
            \tsrv := httptest.NewServer(http.NotFoundHandler())
            \tdefer srv.Close()
            \tresp, err := http.Get(srv.URL + "/probe")
            \tif err != nil {
            \t\tpanic(err)
            \t}
            \tresp.Body.Close()
            }
            """))
        out = self.run_cmd([CTGO, "run", "."], cwd=prog)
        self.assertIn("ctgo: CONFTAMER_EVENTS=", out.stderr)
        [events] = self.events_dir.iterdir()
        kinds = {json.loads(line)["kind"] for line in events.read_text().splitlines()}
        self.assertEqual(kinds, {"Request sent", "Request received", "Response sent",
                                 "Response received"})

    def test_ctgo_build_creates_no_capture(self):
        out = self.run_cmd([CTGO, "list", "-f", "{{.GoFiles}}", "net/http"])
        self.assertIn("conftamer.go", out.stdout)
        self.assertFalse(self.events_dir.exists())


if __name__ == "__main__":
    unittest.main()
