#!/usr/bin/env python3
"""Same-origin bridge for the spectrum tuning panel.

GET  /*                static panel files from this directory
GET  /api/targets      targets.json without serials, tokens or paths
GET  /api/last-values  last frame/params sent by the panel
POST /api/preview      {"device":"mini2","colors":[...]}
POST /api/preview/off  {"device":"mini2"}

Two transports are supported per target:

* ``api``  (default) — sends one frame to Luna's runtime LED API. The device
  token never leaves this process; Mini 2 gets the stock red calibration
  matrix and the supervisor's R/B swap, Lite 2 is plain logical RGB.
* ``file`` — for Station 2, whose 89-pixel ring is a framebuffer owned by the
  AirPlay plugin. The frame is written as JSON over ADB to the path the plugin
  watches; the plugin holds the ring until the file is removed.

Run it on a trusted machine only: it shells out to adb and can write to the
speakers it is configured for.
"""
import datetime
import json
import os
import re
import subprocess
import tempfile
import urllib.request
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

BASE = Path(__file__).resolve().parent
VALUES = BASE / "panel-values.json"
COLOR = re.compile(r"^#[0-9A-Fa-f]{6}$")

CONFIG = BASE / "targets.json"


def load_targets():
    if not CONFIG.is_file():
        return {}
    data = json.loads(CONFIG.read_text())
    targets = data.get("targets")
    if not isinstance(targets, dict):
        raise SystemExit("targets.json: object 'targets' required")
    for name, target in targets.items():
        if not isinstance(target, dict):
            raise SystemExit("targets.json: %s must be an object" % name)
        if not isinstance(target.get("serial"), str) or not target["serial"]:
            raise SystemExit("targets.json: %s.serial required" % name)
        if not isinstance(target.get("leds"), int) or target["leds"] <= 0:
            raise SystemExit("targets.json: %s.leds must be positive" % name)
        transport = target.get("transport", "api")
        if transport not in ("api", "file"):
            raise SystemExit("targets.json: %s.transport must be api or file" % name)
        target["transport"] = transport
        if transport == "file":
            if not isinstance(target.get("path"), str) or not target["path"]:
                raise SystemExit("targets.json: %s.path required for transport=file" % name)
        else:
            if not isinstance(target.get("api"), str) or not target["api"]:
                raise SystemExit("targets.json: %s.api required for transport=api" % name)
        if target.get("matrix") is not None and not isinstance(target["matrix"], str):
            raise SystemExit("targets.json: %s.matrix must be a profile name or null" % name)
        for key in ("black_floor", "saturation"):
            if key in target:
                try:
                    target[key] = float(target[key])
                except (TypeError, ValueError):
                    raise SystemExit("targets.json: %s.%s must be a number" % (name, key))
        target.setdefault("label", name)
        target["swap"] = bool(target.get("swap"))
        target["neutral_black"] = bool(target.get("neutral_black"))
        target["neutral_gain"] = bool(target.get("neutral_gain"))
    return targets


TARGETS = load_targets()

# Fallback copy of /system/vendor/quasar/ledconfigs/red.json. The device copy
# is read first so a firmware update cannot silently change the preview math.
RED_MATRIX = [
    [2.29906108e-01, -8.94722889e-02, -2.80530642e-02],
    [2.75730587e-02, 5.49507443e+00, 4.07274652e-01],
    [-7.40857510e-05, 1.53779975e-02, 1.00720711e+00],
]


def adb_shell(serial, command, timeout=12):
    p = subprocess.run(
        ["adb", "-s", serial, "shell", command],
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    return p.returncode, p.stdout + p.stderr


def device_token(serial):
    rc, out = adb_shell(serial, "pidof station-airplay")
    pid = out.strip().split()[0] if out.strip() else ""
    if rc != 0 or not pid.isdigit():
        raise RuntimeError("station-airplay не запущен на выбранной колонке")
    rc, env = adb_shell(serial, "cat /proc/%s/environ" % pid)
    if rc != 0:
        raise RuntimeError("не удалось прочитать окружение плагина")
    token = url = ""
    for item in env.split("\x00"):
        if item.startswith("LUNA_API_TOKEN="):
            token = item.split("=", 1)[1]
        elif item.startswith("LUNA_API_URL="):
            url = item.split("=", 1)[1]
    if not token or not url.startswith("http://"):
        raise RuntimeError("в окружении плагина нет LUNA_API_TOKEN/LUNA_API_URL")
    return token


def device_matrix(serial, profile):
    if not profile:
        return None
    rc, out = adb_shell(
        serial, "cat /system/vendor/quasar/ledconfigs/%s.json" % profile
    )
    if rc != 0:
        if profile == "red":
            return RED_MATRIX
        raise RuntimeError("LED profile %s is missing on the device" % profile)
    try:
        data = json.loads(out)
        matrix = data["matrix"]
        if data.get("type") != "matrix" or len(matrix) != 3:
            raise ValueError("bad profile")
        if any(len(row) != 3 for row in matrix):
            raise ValueError("bad profile")
        return [[float(x) for x in row] for row in matrix]
    except Exception:
        if profile == "red":
            return RED_MATRIX
        raise RuntimeError("LED profile %s is invalid" % profile)


def apply_matrix(rgb, matrix):
    peak = max(rgb)
    out = []
    for row in matrix:
        value = sum(row[j] * rgb[j] for j in range(3))
        out.append(max(0.0, value))
    high = max(out)
    if high > 0:
        out = [value * peak / high for value in out]
    return [int(round(value)) for value in out]


def pack(color, target, matrix=None):
    value = int(color[1:], 16)
    rgb = [(value >> 16) & 255, (value >> 8) & 255, value & 255]
    if matrix is not None:
        rgb = apply_matrix(rgb, matrix)
    if target["swap"]:
        # Luna's Mini 2 profile packs B,G,R; the supervisor pre-swaps R/B.
        rgb = [rgb[2], rgb[1], rgb[0]]
    return "#%02X%02X%02X" % tuple(rgb)


def api_post(target, path, payload, token):
    req = urllib.request.Request(
        target["api"] + path,
        data=json.dumps(payload).encode(),
        headers={"X-Luna-Token": token, "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=6) as response:
        return response.status, response.read(200).decode(errors="replace")


def file_preview(target, colors):
    """Push one frame to a plugin-owned framebuffer target over ADB."""
    payload = json.dumps({"colors": colors}, ensure_ascii=False) + "\n"
    tmp = tempfile.NamedTemporaryFile(
        "w", suffix=".json", delete=False, encoding="utf-8"
    )
    try:
        tmp.write(payload)
        tmp.close()
        p = subprocess.run(
            ["adb", "-s", target["serial"], "push", tmp.name, target["path"]],
            capture_output=True,
            text=True,
            timeout=20,
        )
        if p.returncode != 0:
            raise RuntimeError((p.stdout + p.stderr).strip() or "adb push failed")
    finally:
        try:
            Path(tmp.name).unlink()
        except OSError:
            pass


def file_off(target):
    rc, out = adb_shell(target["serial"], "rm -f " + target["path"], timeout=12)
    if rc != 0:
        raise RuntimeError(out.strip() or "adb rm failed")


def alsa_playing(serial):
    rc, out = adb_shell(
        serial,
        "grep -q RUNNING /proc/asound/card*/pcm*p/sub0/status && echo yes || echo no",
        timeout=8,
    )
    return out.strip().endswith("yes")


def target_from(data):
    name = data.get("device")
    if not name:
        raise ValueError("не выбрана колонка")
    if name not in TARGETS:
        raise ValueError("нет цели %s в targets.json" % name)
    return name, TARGETS[name]


def save_values(name, colors, sent, params):
    if not isinstance(params, dict):
        return False
    record = {
        "at": datetime.datetime.now().isoformat(timespec="seconds"),
        "device": name,
        "params": params,
        "colors": colors,
        "sent": sent,
    }
    try:
        VALUES.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n")
        return True
    except Exception:
        return False


class Handler(SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=str(BASE), **kwargs)

    def log_message(self, fmt, *args):
        pass

    def _json(self, status, payload):
        body = json.dumps(payload, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/api/targets":
            public = {
                name: {
                    "label": target.get("label", name),
                    "leds": target["leds"],
                    "neutral_black": bool(target.get("neutral_black")),
                    "neutral_gain": bool(target.get("neutral_gain")),
                    "black_floor": float(target.get("black_floor") or 0),
                    "saturation": float(target.get("saturation") or 1),
                    "hold": target.get("transport") == "file",
                    "matrix": target.get("matrix"),
                }
                for name, target in TARGETS.items()
            }
            return self._json(200, {"targets": public})
        if self.path == "/api/last-values":
            if VALUES.is_file():
                try:
                    return self._json(200, json.loads(VALUES.read_text()))
                except Exception:
                    return self._json(500, {"ok": False, "error": "файл значений повреждён"})
            return self._json(404, {"ok": False, "error": "значений ещё нет"})
        return super().do_GET()

    def _check_preview(self, data, target):
        colors = data.get("colors")
        if not isinstance(colors, list) or len(colors) != target["leds"]:
            return None, "нужно ровно %d цветов" % target["leds"]
        if any(not isinstance(c, str) or not COLOR.match(c) for c in colors):
            return None, "цвет должен быть #RRGGBB"
        return colors, None

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length < 0 or length > 8192:
                return self._json(413, {"ok": False, "error": "слишком большой запрос"})
            raw = self.rfile.read(length) if length else b"{}"
            data = json.loads(raw.decode() or "{}")
            name, target = target_from(data)

            if target["transport"] == "file":
                return self._file_request(data, name, target)

            token = device_token(target["serial"])

            if self.path == "/api/preview/off":
                api_post(target, "/led/off", {}, token)
                return self._json(200, {"ok": True, "device": name, "off": True})

            if self.path != "/api/preview":
                return self._json(404, {"ok": False, "error": "неизвестный путь"})

            colors, error = self._check_preview(data, target)
            if error:
                return self._json(400, {"ok": False, "error": error})

            matrix = device_matrix(target["serial"], target.get("matrix"))
            sent = [pack(c, target, matrix) for c in colors]
            saved = save_values(name, colors, sent, data.get("params"))
            api_post(target, "/led", {"colors": sent}, token)
            return self._json(
                200,
                {
                    "ok": True,
                    "device": name,
                    "sent": sent,
                    "playing": alsa_playing(target["serial"]),
                    "params_saved": saved,
                },
            )
        except Exception as exc:
            self._json(502, {"ok": False, "error": str(exc)})

    def _file_request(self, data, name, target):
        if self.path == "/api/preview/off":
            file_off(target)
            return self._json(200, {"ok": True, "device": name, "off": True})

        if self.path != "/api/preview":
            return self._json(404, {"ok": False, "error": "неизвестный путь"})

        colors, error = self._check_preview(data, target)
        if error:
            return self._json(400, {"ok": False, "error": error})

        saved = save_values(name, colors, colors, data.get("params"))
        file_preview(target, colors)
        return self._json(
            200,
            {
                "ok": True,
                "device": name,
                "sent": colors,
                "playing": alsa_playing(target["serial"]),
                "params_saved": saved,
                "preview": True,
            },
        )


def main():
    host = os.environ.get("PANEL_HOST", "0.0.0.0")
    port = int(os.environ.get("PANEL_PORT", "8799"))
    server = ThreadingHTTPServer((host, port), Handler)
    print("panel bridge on http://%s:%d" % (host, port), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
