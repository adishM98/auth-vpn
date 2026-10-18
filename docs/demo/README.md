# Demo recordings

← [README](../../README.md)

How the clips in the README are made, so they can be re-recorded when the CLI or the dashboard changes. Everything runs in throwaway Docker containers: documentation-range IPs, fresh tokens, nothing real.

| Clip | Made with | Source |
|---|---|---|
| `media/terminal.gif` | [VHS](https://github.com/charmbracelet/vhs), inside the demo "laptop" container | `terminal.tape` |
| `media/dashboard.gif` | cut from a recording made with [Argo](https://github.com/shreyaskarnik/argo) (Playwright plus a local Kokoro voiceover that paces the scenes) | `tour/demos/tour.*` |

## The environment

`env.sh up` builds the image in `Dockerfile` (auth-vpn, VHS, Chromium, psql, tmux) and starts:

- **stage** server at `203.0.113.10`, pushing the route to its private network `10.20.0.0/24`, with Postgres at `10.20.0.5`. Its dashboard is published on `https://localhost:19100/ui`.
- **prod** server at `198.51.100.7`, private network `10.30.0.0/24`, Postgres at `10.30.0.5`.
- a **laptop** container on both public networks. It can reach the private networks only through auth-vpn.

Tokens and the stage API key are written to `.env` (gitignored).

## Re-recording

Needs Docker and Node 20+.

```bash
docs/demo/env.sh up            # build and start (about 2 min the first time)
docs/demo/env.sh tape          # media/terminal.gif + .mp4
docs/demo/env.sh dash          # put a live client on stage for the dashboard
cd docs/demo/tour && npm ci && npx argo pipeline tour   # videos/tour.mp4, not committed
# dashboard GIF: the dashboard half of the tour (start/length from the scene table Argo prints)
ffmpeg -ss <start> -i videos/tour.mp4 -t <length> \
  -vf "fps=12,scale=1000:-1:flags=lanczos,split[a][b];[a]palettegen[p];[b][p]paletteuse" ../media/dashboard.gif
docs/demo/env.sh down
```

If the terminal recording's timing changes, update the segment times (`SEG`) in `tour/demos/tour.demo.ts`. They are the seconds in `terminal.mp4` where each step of `terminal.tape` starts and ends.
