# Proclaim — jadeck add-on

Next / previous, go to a service item, blank, and see what's on air.

## Quick setup

1. In Proclaim: **Settings → Remote → Local Server → Enable** (port 52195).
2. On another PC, note the **Network Control Password** on the same page
   (not needed when jadeck runs on the Proclaim PC).
3. In jadeck: Settings → Connections → Add → Proclaim.
4. Open the **Proclaim PC address** list: Proclaim PCs on this computer's
   networks are found in a few seconds ("192.168.1.20 — Proclaim on
   office-pc (on air: no)"). Pick one, or type the address (Tailscale and
   VPN addresses are not scanned).
5. Enter the password, then press **Test**: it answers in one line, and
   says what to change if something is off (for example "In Proclaim:
   Settings → Remote → Local Server → Enable").

Install: it ships with the controller (Settings → Add-ons); newer releases of this repository arrive as updates there.

Build every platform and the `.ptzaddon` package:

```bash
go run github.com/toolingdev/jadeck/sdk/addon/cmd/ptz-addon build
```

Try it on a controller: Settings → Add-ons → Developer → Load a folder (this folder),
or install `dist/proclaim.ptzaddon` with Install from file.

Release: push a tag `v1.2.3`; the workflow builds, signs (secret `PTZ_SIGNING_KEY`)
and attaches `proclaim.ptzaddon` to the GitHub release.
