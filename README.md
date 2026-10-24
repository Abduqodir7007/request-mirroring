# request-mirroring — Claude Code skill

Safely replace a backend service (Python → Go, v1 → v2, monolith → service) by mirroring **real production traffic** to the new implementation and comparing every response — without the new one ever answering a user or touching production data.

```
client ─► nginx ─► shadow proxy ──► PRIMARY (old)   → answers the client
             │            └─ copy ─► SHADOW (new)    → only compared, own DB copy + fake external APIs
             └─ proxy down → nginx backup → PRIMARY
```

What you get: parity % per endpoint, a list of every differing response (both bodies side by side), p50/p95/p99 of both, RAM/CPU of both — in Prometheus/Grafana — plus one command to switch the mirror on or off.

Built and used for a real Python (Django) → Go migration of three production services.

## Install

```bash
# personal skill (all projects)
git clone https://github.com/Abduqodir7007/request-mirroring.git ~/.claude/skills/request-mirroring

# or project skill (this repo only)
git clone https://github.com/Abduqodir7007/request-mirroring.git .claude/skills/request-mirroring
```

Update later with `git -C ~/.claude/skills/request-mirroring pull`.

Then just ask Claude Code something like *"set up request mirroring from the Python core service to the new Go one"* — the skill loads automatically (or type `/request-mirroring`).

## Contents

| path | |
|---|---|
| `SKILL.md` | entry point for Claude: rules, procedure, how to read results |
| `references/architecture.md` | proxy internals, comparison, metrics, settings, fake APIs |
| `references/rollout-runbook.md` | phase 0–7 + cutover, with commands |
| `references/shadow-data.md` | shadow DB / Redis / network, periodic refresh |
| `references/pitfalls.md` | real mistakes and how to avoid them |
| `templates/` | compose files, nginx configs, switch/check/refresh scripts, Prometheus jobs, Grafana dashboard generator |
| `tool/` | Go source of `proxy`, `faketg`, `cgstats` (stdlib only, tests included) |

## Build the tool images

```bash
cd tool
docker build --target proxy  -t shadow_proxy:latest .
docker build --target faketg -t shadow_faketg:latest .
go test ./...
```

---

## O'zbekcha qisqacha

Bu skill eski servisni (masalan Python) yangisiga (masalan Go) xavfsiz almashtirish uchun. Prod'dagi har bir so'rovning nusxasi yangi servisga ham yuboriladi va javoblar solishtiriladi. Mijoz esa doim eski servis javobini oladi. Yangi servis bazaning alohida nusxasi va soxta tashqi API'lar (Telegram, AI, webhook) bilan ishlaydi, shuning uchun hech kimga xabar yubormaydi va prod bazaga yozmaydi.

O'rnatish: yuqoridagi `git clone` buyrug'i bilan `~/.claude/skills/request-mirroring` ga yuklab oling. Keyin Claude Code'ga "Python core servisidan Go'ga request mirroring sozla" deb yozsangiz bo'ladi. Yangilash: `git -C ~/.claude/skills/request-mirroring pull`.

## License

MIT
